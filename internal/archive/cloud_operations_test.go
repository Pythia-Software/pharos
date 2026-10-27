package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/klauspost/compress/zstd"
)

func cloudFixture(t *testing.T, protected bool) (*Catalog, Config, cloudDestination, cloudRun, string) {
	t.Helper()
	c, _ := testCatalog(t)
	root := t.TempDir()
	config := Config{StagingRoot: filepath.Join(root, "staging")}
	if err := os.Mkdir(config.StagingRoot, 0700); err != nil {
		t.Fatal(err)
	}
	p := cloudConfig{Provider: "gcp", Region: "us-central1", Name: "Test destination", Bucket: "pharos-test-bucket", Object: "pharos/library.tar.zst", Identity: "pharos-uploader", Project: "pharos-example", StorageClass: "STANDARD", PasswordProtected: protected}
	d := cloudDestination{ImportedAt: now(), plan: p, credential: []byte(`{"type":"service_account","private_key":"credential-fixture"}`)}
	if protected {
		key, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		recipient, err := age.NewScryptRecipient("correct recovery password")
		if err != nil {
			t.Fatal(err)
		}
		var wrapped bytes.Buffer
		armored := armor.NewWriter(&wrapped)
		encrypted, err := age.Encrypt(armored, recipient)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(encrypted, key.String()+"\n"); err != nil {
			t.Fatal(err)
		}
		if err = encrypted.Close(); err != nil {
			t.Fatal(err)
		}
		if err = armored.Close(); err != nil {
			t.Fatal(err)
		}
		d.plan.PublicRecipient = key.Recipient().String()
		d.plan.EncryptionSetup = "verified"
		d.recovery = &cloudRecovery{Version: 1, Format: "age-password-recovery-v1", Recipient: d.plan.PublicRecipient, EncryptedIdentity: wrapped.String()}
	}
	d.ID = cloudID(d.plan)
	d.Config, _ = json.Marshal(d.plan)
	if err := c.saveCloudDestination(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	run := cloudRun{ID: randomToken(), DestinationID: d.ID, Kind: "sample", State: "running", Object: p.Object + ".roundtrip-test", StartedAt: now()}
	stage := filepath.Join(root, "upload")
	if err := os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	archive, manifest, err := buildCloudArchive(context.Background(), d, &run, config, stage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	run.State = "complete"
	run.Version = "fixture-generation"
	if err = c.saveCloudRun(context.Background(), &run, &manifest); err != nil {
		t.Fatal(err)
	}
	return c, config, d, run, archive
}
func TestCloudArchiveRoundTripAndChangedDownload(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "password"}[protected], func(t *testing.T) {
			if protected {
				slowTest(t)
			}
			c, config, d, run, archive := cloudFixture(t, protected)
			ctx := context.Background()
			// Public destination responses must not contain the credential or identity.
			public, err := c.cloudDestinations(ctx)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(public)
			if bytes.Contains(raw, []byte("credential-fixture")) || bytes.Contains(raw, []byte("AGE-SECRET-KEY")) {
				t.Fatal("private key material reached public destination response")
			}
			if protected {
				if err = c.finishCloudVerification(ctx, config, &run, archive, []byte("wrong password")); err == nil || run.Verified {
					t.Fatal("wrong password passed recovery verification")
				}
			}
			if err = c.finishCloudVerification(ctx, config, &run, archive, []byte("correct recovery password")); err != nil {
				t.Fatal(err)
			}
			stored, err := c.cloudRun(ctx, run.ID)
			if err != nil || !stored.Verified {
				t.Fatalf("verification not persisted: %+v %v", stored, err)
			}
			recovered, err := os.ReadFile(filepath.Join(stored.RestoredDirectory, "round-trip-test.bin"))
			if err != nil || len(recovered) != 1<<20 {
				t.Fatalf("missing recovered sample: %v", err)
			}
			var manifestJSON string
			if err = c.reader().QueryRow("SELECT manifest_json FROM cloud_backup_runs WHERE id=?", run.ID).Scan(&manifestJSON); err != nil {
				t.Fatal(err)
			}
			var manifest cloudManifest
			_ = json.Unmarshal([]byte(manifestJSON), &manifest)
			hash := sha256.Sum256(recovered)
			if manifest.Files[0].SHA256 != hex.EncodeToString(hash[:]) {
				t.Fatal("recovered bytes differ from the upload manifest")
			}
			data, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			data[len(data)/2] ^= 1
			damaged := filepath.Join(t.TempDir(), "damaged")
			if err = os.WriteFile(damaged, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err = c.finishCloudVerification(ctx, config, &run, damaged, []byte("correct recovery password")); err == nil || run.Verified {
				t.Fatal("changed downloaded bytes passed verification")
			}
			// Disaster recovery uses the archive's embedded bundle and manifest,
			// without any destination or upload-ledger lookup.
			input, e := os.Open(archive)
			if e != nil {
				t.Fatal(e)
			}
			payload, e := cloudPayload(input, []byte("correct recovery password"), "")
			if e != nil {
				input.Close()
				t.Fatal(e)
			}
			standalone, e := extractCloudArchive(ctx, config, payload, nil)
			input.Close()
			if e != nil {
				t.Fatal(e)
			}
			if _, e = os.Stat(filepath.Join(standalone, "round-trip-test.bin")); e != nil {
				t.Fatal(e)
			}
			// A failure must not remove a previously recovered copy.
			if _, err = os.Stat(stored.RestoredDirectory); err != nil {
				t.Fatal(err)
			}
			again, err := c.cloudDestination(ctx, d.ID, true)
			if err != nil || !bytes.Equal(again.credential, d.credential) {
				t.Fatal("verification changed the upload credential")
			}
			// The snapshot fits, but the compressed output must stop before it
			// consumes the space reserved for concurrent library writes. Model a
			// small remaining budget instead of filling a real disk.
			limited := t.TempDir()
			archivePath := filepath.Join(limited, "archive")
			budget := int64(512 << 10)
			noSpace := errors.New("archive output would consume the free-space reserve")
			var refusedAt int64 = -1
			override(t, &cloudArchiveSpace, func(dir string, size int64) error {
				if dir != limited {
					t.Fatalf("space checked on the wrong output volume: %s", dir)
				}
				info, err := os.Stat(archivePath)
				if err != nil {
					return err
				}
				if info.Size()+size > budget {
					refusedAt = info.Size()
					return noSpace
				}
				return nil
			})
			limitedRun := cloudRun{ID: randomToken(), Kind: "sample"}
			if _, _, err = buildCloudArchive(ctx, d, &limitedRun, config, limited, func(string) {}); !errors.Is(err, noSpace) {
				t.Fatalf("archive did not stop at its space budget: %v", err)
			}
			info, err := os.Stat(archivePath)
			if err != nil || refusedAt <= 0 || info.Size() != refusedAt || info.Size() > budget {
				t.Fatalf("compression wrote past the reserved space or flushed after failure: %v, refused at %d", err, refusedAt)
			}
		})
	}
}
func TestCloudManifestRejectsEscapingPaths(t *testing.T) {
	for _, test := range []struct {
		name, target string
		version      int
		valid        bool
	}{
		{"valid-link", "seed", 2, true}, {"escaping-link", "../outside", 2, false}, {"forward-link", "later", 2, false}, {"legacy-link", "seed", 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, config, _, _, _ := cloudFixture(t, false)
			var data bytes.Buffer
			compressed, err := zstd.NewWriter(&data, zstd.WithEncoderConcurrency(1))
			if err != nil {
				t.Fatal(err)
			}
			writer := tar.NewWriter(compressed)
			body := "retained bytes"
			hash := sha256.Sum256([]byte(body))
			digest := hex.EncodeToString(hash[:])
			if err = writer.WriteHeader(&tar.Header{Name: "seed", Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err = io.WriteString(writer, body); err != nil {
				t.Fatal(err)
			}
			if err = writer.WriteHeader(&tar.Header{Name: "alias", Mode: 0600, Typeflag: tar.TypeLink, Linkname: test.target}); err != nil {
				t.Fatal(err)
			}
			manifest := cloudManifest{Version: test.version, Kind: "sample", Files: []cloudFile{{Path: "seed", Size: int64(len(body)), SHA256: digest}, {Path: "alias", Size: int64(len(body)), SHA256: digest, Link: test.target}}}
			raw, _ := json.Marshal(manifest)
			if err = writer.WriteHeader(&tar.Header{Name: cloudManifestName, Mode: 0600, Size: int64(len(raw)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err = writer.Write(raw); err != nil {
				t.Fatal(err)
			}
			if err = writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err = compressed.Close(); err != nil {
				t.Fatal(err)
			}
			restored, err := extractCloudArchive(t.Context(), config, bytes.NewReader(data.Bytes()), nil)
			if !test.valid {
				if err == nil {
					t.Fatal("unsafe archive link accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if content, err := os.ReadFile(filepath.Join(restored, "alias")); err != nil || string(content) != body {
				t.Fatal("hard-linked restore lost bytes", err)
			}
		})
	}

	c, config, _, run, archive := cloudFixture(t, false)
	var raw string
	if err := c.reader().QueryRow("SELECT manifest_json FROM cloud_backup_runs WHERE id=?", run.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var manifest cloudManifest
	_ = json.Unmarshal([]byte(raw), &manifest)
	manifest.Files[0].Path = "../outside"
	if err := c.saveCloudRun(context.Background(), &run, &manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := c.verifyCloudArchive(context.Background(), config, &run, archive, nil); err == nil {
		t.Fatal("escaping manifest path was accepted")
	}
	if _, err := os.Stat(filepath.Join(config.StagingRoot, "outside")); !os.IsNotExist(err) {
		t.Fatal("restore wrote outside its isolated directory")
	}
}

type cloudRoundTripper func(*http.Request) (*http.Response, error)

func (f cloudRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCloudCredentialChecksRefuseReadOrListAccess(t *testing.T) {
	for _, statuses := range [][]int{{403, 403}, {200, 403}, {403, 200}, {401, 403}, {404, 403}} {
		index := 0
		client := &http.Client{Transport: cloudRoundTripper(func(r *http.Request) (*http.Response, error) {
			status := statuses[min(index, 1)]
			index++
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"errors":[{"reason":"forbidden"}]}}`)), Request: r}, nil
		})}
		uploader := &cloudUploader{dest: cloudDestination{plan: cloudConfig{Bucket: "bucket-test"}}, google: client}
		err := uploader.checkRestricted(context.Background(), "probe")
		allowed := statuses[0] == 403 && statuses[1] == 403
		if (err == nil) != allowed {
			t.Fatalf("statuses %v: %v", statuses, err)
		}
	}
}
func TestCloudConfigurationCannotExposeUnknownSecretFields(t *testing.T) {
	public, err := cloudPublicConfig([]byte(`{"name":"Backup","password":"never-return-this","credential_json":"private","public_recipient":"age1example"}`))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(public, []byte("never-return-this")) || bytes.Contains(public, []byte("private")) {
		t.Fatal("unrecognized secrets returned to browser")
	}
}

func TestCloudFullSnapshotDoesNotClaimLocalBackup(t *testing.T) {
	slowTest(t)
	config, _ := backupFixture(t)
	source := filepath.Join(config.CaptureRoot, "host-a", "claude", "files", "p", "b.jsonl")
	alias := filepath.Join(config.CaptureRoot, "host-a", "claude", "inputs", "revision", "files", "b.jsonl")
	if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, alias); err != nil {
		t.Fatal(err)
	}
	catalog := reopenCatalog(t, config.CatalogPath)
	if err := os.MkdirAll(config.StagingRoot, 0700); err != nil {
		t.Fatal(err)
	}
	p := cloudConfig{Provider: "gcp", Region: "us-central1", Bucket: "pharos-test-bucket", Object: "pharos/library.tar.zst", StorageClass: "STANDARD"}
	d := cloudDestination{plan: p, ImportedAt: now(), credential: []byte(`{"private":"upload-only"}`)}
	d.ID = cloudID(p)
	d.Config, _ = json.Marshal(p)
	if err := catalog.saveCloudDestination(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	run := cloudRun{ID: randomToken(), DestinationID: d.ID, Kind: "full", State: "running", StartedAt: now()}
	stage, err := cloudWorkDir(config, "cloud-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	archive, manifest, err := buildCloudArchive(context.Background(), d, &run, config, stage, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 2 {
		t.Fatal("hard-linked snapshot did not use compact archive format", manifest.Version)
	}
	var aliases int
	for _, file := range manifest.Files {
		if file.Link != "" {
			aliases++
		}
	}
	if aliases != 1 {
		t.Fatal("archive duplicated the retained inode", aliases)
	}
	if len(loadBackupHistory(config).Backups) != 0 {
		t.Fatal("temporary cloud staging was recorded as a durable local backup")
	}
	run.State = "complete"
	run.Version = "fixture-version"
	if err = catalog.saveCloudRun(context.Background(), &run, &manifest); err != nil {
		t.Fatal(err)
	}
	if err = catalog.finishCloudVerification(context.Background(), config, &run, archive, nil); err != nil {
		t.Fatal(err)
	}
	if !run.Verified || run.Files < 3 {
		t.Fatal("full snapshot did not validate its catalog and source files")
	}
}

func TestCloudGCSResolvesLostCompletionWithoutReadAccess(t *testing.T) {
	for _, scenario := range []string{"normal", "lost-reply", "wrong-checksum"} {
		t.Run(scenario, func(t *testing.T) {
			data := bytes.Repeat([]byte("independent full archive\n"), 128)
			path := filepath.Join(t.TempDir(), "archive")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			var sum [4]byte
			binary.BigEndian.PutUint32(sum[:], crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)))
			checksum := base64.StdEncoding.EncodeToString(sum[:])
			if scenario == "wrong-checksum" {
				checksum = "wrong"
			}
			uploaded, statusQueries := 0, 0
			client := &http.Client{Transport: cloudRoundTripper(func(req *http.Request) (*http.Response, error) {
				headers := http.Header{}
				body := ""
				status := 200
				switch {
				case req.Method == "POST":
					headers.Set("Location", "https://storage.googleapis.com/resumable-session")
				case req.Method == "PUT" && strings.HasPrefix(req.Header.Get("Content-Range"), "bytes */"):
					statusQueries++
					raw, _ := json.Marshal(map[string]string{"generation": "real-generation", "crc32c": checksum})
					body = string(raw)
				case req.Method == "PUT":
					uploaded++
					received, err := io.ReadAll(req.Body)
					if err != nil || !bytes.Equal(received, data) {
						t.Fatal("upload bytes changed")
					}
					if scenario == "lost-reply" {
						return nil, errors.New("completion response was lost")
					}
					raw, _ := json.Marshal(map[string]string{"generation": "real-generation", "crc32c": checksum})
					body = string(raw)
				default:
					t.Fatalf("upload attempted a read request: %s", req.Method)
				}
				return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			})}
			override(t, &cloudHTTP, client)
			override(t, &cloudUploadRetryDelay, time.Duration(0))
			u := &cloudUploader{dest: cloudDestination{plan: cloudConfig{Provider: "gcp", Bucket: "pharos-test", StorageClass: "STANDARD"}}, google: client}
			var done int64
			version, err := u.upload(context.Background(), "archive", path, func(n int64) { done = n })
			if scenario == "wrong-checksum" {
				if err == nil {
					t.Fatal("provider checksum mismatch was accepted")
				}
				return
			}
			if err != nil || version != "real-generation" || done != int64(len(data)) {
				t.Fatalf("completion was not recorded: %q %d %v", version, done, err)
			}
			if uploaded != 1 {
				t.Fatal("a lost completion was replayed as another object upload")
			}
			if (statusQueries == 1) != (scenario == "lost-reply") {
				t.Fatal("resumable status was not resolved")
			}
		})
	}
}

func TestCloudDisconnectPreservesRestoreEvidence(t *testing.T) {
	catalog, config, d, run, archive := cloudFixture(t, false)
	if err := catalog.forgetCloudCredential(context.Background(), d.ID); err != nil {
		t.Fatal(err)
	}
	connected, err := catalog.cloudDestination(context.Background(), d.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if connected.Connected || len(connected.credential) != 0 {
		t.Fatal("upload credential survived disconnect")
	}
	if err = catalog.finishCloudVerification(context.Background(), config, &run, archive, nil); err != nil {
		t.Fatalf("disconnect removed recovery evidence: %v", err)
	}
}

func TestCloudRestoresSQLiteRecordReplay(t *testing.T) {
	slowTest(t)
	for _, protected := range []bool{false, true} {
		t.Run(fmt.Sprint(protected), func(t *testing.T) {
			_, _, destination, run, _ := cloudFixture(t, protected)
			config, _ := backupFixture(t)
			original := addSQLiteRecordBackupFixture(t, &config)
			if e := os.MkdirAll(config.StagingRoot, 0700); e != nil {
				t.Fatal(e)
			}
			stage, e := cloudWorkDir(config, "cloud-records-test-")
			if e != nil {
				t.Fatal(e)
			}
			defer os.RemoveAll(stage)
			run.Kind = "full"
			archive, _, e := buildCloudArchive(t.Context(), destination, &run, config, stage, func(string) {})
			if e != nil {
				t.Fatal(e)
			}
			f, e := os.Open(archive)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			payload, e := cloudPayload(f, []byte("correct recovery password"), "")
			if e != nil {
				t.Fatal(e)
			}
			restored, e := extractCloudArchive(t.Context(), config, payload, nil)
			if e != nil {
				t.Fatal(e)
			}
			os.RemoveAll(original)
			config.CaptureRoot = filepath.Join(restored, "captures")
			os.RemoveAll(capturedPath(config, "records", "files"))
			os.RemoveAll(capturedPath(config, "records", "history"))
			catalog, _ := testCatalog(t)
			recordTestReplay(t, config, "records", catalog)
			if conversationMessages(t, catalog, "conductor:durable-backup") != 2 {
				t.Fatal("cloud archive lost raw SQLite evidence")
			}
		})
	}
}
