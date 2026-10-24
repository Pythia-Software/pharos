package archive

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"golang.org/x/oauth2/jwt"
)

var cloudUploadRetryDelay = time.Second

// No automatic redirects: resumable session URLs and SAS tokens are credentials.
var cloudHTTP = &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type cloudUploader struct {
	dest   cloudDestination
	s3     *s3.Client
	google *http.Client
	azure  url.Values
}

func newCloudUploader(ctx context.Context, d cloudDestination) (*cloudUploader, error) {
	u := &cloudUploader{dest: d}
	switch p := d.plan; p.Provider {
	case "aws":
		var key struct {
			UserName    string `json:"UserName"`
			AccessKeyID string `json:"AccessKeyId"`
			Secret      string `json:"SecretAccessKey"`
			Session     string `json:"SessionToken"`
		}
		if json.Unmarshal(d.credential, &key) != nil || key.AccessKeyID == "" || key.Secret == "" || key.UserName != p.Identity {
			return nil, errors.New("invalid AWS upload credential file")
		}
		u.s3 = s3.New(s3.Options{Region: p.Region, Credentials: credentials.NewStaticCredentialsProvider(key.AccessKeyID, key.Secret, key.Session), HTTPClient: cloudHTTP, RetryMaxAttempts: 3})
	case "gcp":
		var key struct {
			Type       string `json:"type"`
			Email      string `json:"client_email"`
			PrivateKey string `json:"private_key"`
			KeyID      string `json:"private_key_id"`
		}
		if json.Unmarshal(d.credential, &key) != nil || key.Type != "service_account" || key.Email != p.Identity+"@"+p.Project+".iam.gserviceaccount.com" || key.PrivateKey == "" {
			return nil, errors.New("credential is not the configured Google upload service account")
		}
		cfg := jwt.Config{Email: key.Email, PrivateKey: []byte(key.PrivateKey), PrivateKeyID: key.KeyID, TokenURL: "https://oauth2.googleapis.com/token", Scopes: []string{"https://www.googleapis.com/auth/devstorage.read_write"}}
		u.google = cfg.Client(ctx)
		u.google.Timeout = 10 * time.Minute
		u.google.CheckRedirect = cloudHTTP.CheckRedirect
	case "azure":
		var key struct {
			SAS string `json:"sas"`
		}
		if json.Unmarshal(d.credential, &key) != nil {
			return nil, errors.New("invalid Azure credential file")
		}
		var err error
		u.azure, err = url.ParseQuery(strings.TrimPrefix(key.SAS, "?"))
		if err != nil || u.azure.Get("sig") == "" {
			return nil, errors.New("invalid Azure upload SAS")
		}
		for _, permission := range u.azure.Get("sp") {
			if permission != 'c' && permission != 'w' {
				return nil, errors.New("SAS must grant only create/write access")
			}
		}
		if u.azure.Get("spr") != "https" {
			return nil, errors.New("Azure SAS must require HTTPS")
		}
	default:
		return nil, errors.New("unsupported cloud provider")
	}
	return u, nil
}
func cloudResponse(client *http.Client, req *http.Request) (*http.Response, error) {
	response, err := client.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		return nil, errors.New("cloud request failed; check connectivity and retry")
	}
	return response, nil
}
func cloudStatus(response *http.Response, allowed ...int) error {
	for _, status := range allowed {
		if response.StatusCode == status {
			return nil
		}
	}
	return fmt.Errorf("cloud request rejected (HTTP %d); check the destination, permissions and billing", response.StatusCode)
}
func cloudPartSize(size int64) int64 {
	part := int64(64 << 20)
	if required := (size + 9999) / 10000; required > part {
		part = (required + (256 << 10) - 1) / (256 << 10) * (256 << 10)
	}
	return part
}

func (u *cloudUploader) upload(ctx context.Context, object, path string, progress func(int64)) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	size := info.Size()
	if size <= 0 || size > 5*(1<<40) {
		return "", errors.New("archive must be between one byte and 5 TiB")
	}
	switch u.dest.plan.Provider {
	case "aws":
		return u.uploadS3(ctx, object, file, size, progress)
	case "gcp":
		return u.uploadGCP(ctx, object, file, size, progress)
	default:
		return u.uploadAzure(ctx, object, file, size, progress)
	}
}
func s3Error(err error) error {
	if err == nil {
		return nil
	}
	var e smithy.APIError
	if errors.As(err, &e) {
		return fmt.Errorf("S3 rejected the upload (%s); check permissions, region and billing", e.ErrorCode())
	}
	return errors.New("S3 upload failed; check connectivity and retry")
}
func (u *cloudUploader) uploadS3(ctx context.Context, object string, file *os.File, size int64, progress func(int64)) (string, error) {
	p := u.dest.plan
	start, err := u.s3.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: &p.Bucket, Key: &object, StorageClass: s3types.StorageClass(p.StorageClass), ContentType: aws.String("application/octet-stream"), ChecksumAlgorithm: s3types.ChecksumAlgorithmSha256})
	if err != nil {
		return "", s3Error(err)
	}
	if start.UploadId == nil {
		return "", errors.New("S3 did not return an upload ID")
	}
	committed := false
	defer func() {
		if !committed {
			cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, _ = u.s3.AbortMultipartUpload(cleanup, &s3.AbortMultipartUploadInput{Bucket: &p.Bucket, Key: &object, UploadId: start.UploadId})
		}
	}()
	var parts []s3types.CompletedPart
	partSize := cloudPartSize(size)
	for offset := int64(0); offset < size; offset += partSize {
		n := min(partSize, size-offset)
		reader := io.NewSectionReader(file, offset, n)
		part := int32(len(parts) + 1)
		result, err := u.s3.UploadPart(ctx, &s3.UploadPartInput{Bucket: &p.Bucket, Key: &object, UploadId: start.UploadId, PartNumber: &part, Body: reader, ContentLength: &n, ChecksumAlgorithm: s3types.ChecksumAlgorithmSha256})
		if err != nil {
			return "", s3Error(err)
		}
		if result.ETag == nil || result.ChecksumSHA256 == nil {
			return "", errors.New("S3 did not acknowledge an upload part")
		}
		parts = append(parts, s3types.CompletedPart{ETag: result.ETag, PartNumber: &part, ChecksumSHA256: result.ChecksumSHA256})
		progress(offset + n)
	}
	// A lost completion response is uncertain; never automatically start another
	// complete request or overwrite the object to hide that uncertainty.
	result, err := u.s3.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: &p.Bucket, Key: &object, UploadId: start.UploadId, MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts}}, func(o *s3.Options) { o.RetryMaxAttempts = 1 })
	if err != nil {
		return "", s3Error(err)
	}
	committed = true
	version := aws.ToString(result.VersionId)
	if version == "" || version == "null" {
		return "", errors.New("upload completed without a version ID; enable bucket versioning before retrying")
	}
	return version, nil
}
func (u *cloudUploader) uploadGCP(ctx context.Context, object string, file *os.File, size int64, progress func(int64)) (string, error) {
	p := u.dest.plan
	metadata, _ := json.Marshal(map[string]string{"name": object, "storageClass": p.StorageClass})
	target := "https://storage.googleapis.com/upload/storage/v1/b/" + url.PathEscape(p.Bucket) + "/o?uploadType=resumable"
	req, _ := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(metadata))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
	req.Header.Set("X-Upload-Content-Type", "application/octet-stream")
	response, err := cloudResponse(u.google, req)
	if err != nil {
		return "", err
	}
	response.Body.Close()
	if err = cloudStatus(response, 200, 201); err != nil {
		return "", err
	}
	session := response.Header.Get("Location")
	endpoint, err := url.Parse(session)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host != "storage.googleapis.com" || endpoint.User != nil {
		return "", errors.New("Google returned an unexpected upload session address")
	}
	partSize := cloudPartSize(size)
	checksum := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	failures := 0
	statusOnly := false
	for offset := int64(0); offset < size; {
		n := min(partSize, size-offset)
		if statusOnly {
			req, _ = http.NewRequestWithContext(ctx, "PUT", session, nil)
			req.ContentLength = 0
			req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		} else {
			req, _ = http.NewRequestWithContext(ctx, "PUT", session, io.NewSectionReader(file, offset, n))
			req.ContentLength = n
			req.Header.Set("Content-Type", "application/octet-stream")
			req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+n-1, size))
		}
		response, err = cloudResponse(cloudHTTP, req)
		// A status PUT is part of the write-only resumable protocol. It reads no
		// object data and resolves whether a chunk or final commit reached GCS.
		if err != nil || (response != nil && (response.StatusCode == 429 || response.StatusCode >= 500)) {
			if response != nil {
				response.Body.Close()
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			failures++
			if failures > 3 {
				return "", errors.New("Google upload could not be resumed; retry when the connection is stable")
			}
			timer := time.NewTimer(time.Duration(failures) * cloudUploadRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
			statusOnly = true
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil {
			failures++
			if failures > 3 {
				return "", errors.New("Google completion could not be confirmed")
			}
			statusOnly = true
			continue
		}
		accepted := offset
		if response.StatusCode == 308 {
			rangeHeader := response.Header.Get("Range")
			if rangeHeader != "" {
				var last int64
				if _, e := fmt.Sscanf(rangeHeader, "bytes=0-%d", &last); e != nil {
					return "", errors.New("invalid Google upload acknowledgement")
				}
				accepted = last + 1
			}
		} else if response.StatusCode == 200 || response.StatusCode == 201 {
			accepted = size
		} else {
			return "", cloudStatus(response, 200, 201, 308)
		}
		if accepted < offset || accepted > offset+n {
			return "", errors.New("Google acknowledged an unexpected byte range")
		}
		if accepted == offset {
			statusOnly = false
			failures++
			if failures > 3 {
				return "", errors.New("Google did not persist the upload chunk")
			}
			continue
		}
		// Hash only acknowledged bytes, so retransmission never double-counts data.
		if _, err = cloudCopy(ctx, checksum, io.NewSectionReader(file, offset, accepted-offset)); err != nil {
			return "", err
		}
		statusOnly = false
		offset = accepted
		failures = 0
		progress(offset)
		if offset == size {
			var result struct {
				Generation string `json:"generation"`
				CRC32C     string `json:"crc32c"`
			}
			if json.Unmarshal(data, &result) != nil || result.Generation == "" {
				return "", errors.New("upload completion did not include a generation")
			}
			var sum [4]byte
			binary.BigEndian.PutUint32(sum[:], checksum.Sum32())
			if result.CRC32C != base64.StdEncoding.EncodeToString(sum[:]) {
				return "", errors.New("Google completion checksum did not match the uploaded archive")
			}
			return result.Generation, nil
		}
	}
	return "", errors.New("Google upload ended without a completed object")
}

func (u *cloudUploader) azureURL(object string, params url.Values) string {
	p := u.dest.plan
	q := url.Values{}
	for k, v := range u.azure {
		q[k] = append([]string{}, v...)
	}
	for k, v := range params {
		q[k] = v
	}
	return (&url.URL{Scheme: "https", Host: p.Account + ".blob.core.windows.net", Path: "/" + p.Container + "/" + object, RawQuery: q.Encode()}).String()
}
func (u *cloudUploader) uploadAzure(ctx context.Context, object string, file *os.File, size int64, progress func(int64)) (string, error) {
	var blocks []string
	partSize := cloudPartSize(size)
	nonce := randomToken()
	buffer := make([]byte, partSize)
	for offset := int64(0); offset < size; offset += partSize {
		n := min(partSize, size-offset)
		if _, err := io.ReadFull(file, buffer[:n]); err != nil {
			return "", err
		}
		id := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s-%05d", nonce, len(blocks))))
		digest := md5.Sum(buffer[:n])
		req, _ := http.NewRequestWithContext(ctx, "PUT", u.azureURL(object, url.Values{"comp": {"block"}, "blockid": {id}}), bytes.NewReader(buffer[:n]))
		req.Header.Set("x-ms-version", "2023-11-03")
		req.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(digest[:]))
		response, err := cloudResponse(cloudHTTP, req)
		if err != nil {
			return "", err
		}
		response.Body.Close()
		if err = cloudStatus(response, 201); err != nil {
			return "", err
		}
		blocks = append(blocks, id)
		progress(offset + n)
	}
	type blockList struct {
		XMLName xml.Name `xml:"BlockList"`
		Blocks  []string `xml:"Latest"`
	}
	data, _ := xml.Marshal(blockList{Blocks: blocks})
	req, _ := http.NewRequestWithContext(ctx, "PUT", u.azureURL(object, url.Values{"comp": {"blocklist"}}), bytes.NewReader(data))
	req.Header.Set("x-ms-version", "2023-11-03")
	req.GetBody = nil // committing a block list must not be retried after a lost response
	req.Header.Set("x-ms-access-tier", u.dest.plan.StorageClass)
	req.Header.Set("x-ms-blob-content-type", "application/octet-stream")
	response, err := cloudResponse(cloudHTTP, req)
	if err != nil {
		return "", err
	}
	response.Body.Close()
	if err = cloudStatus(response, 201); err != nil {
		return "", err
	}
	version := response.Header.Get("x-ms-version-id")
	if version == "" {
		return "", errors.New("Azure upload completed without a version ID; enable blob versioning")
	}
	return version, nil
}

// Test permissions before storing a credential. The sample is random, with no
// library content. List responses request zero/one entries and are discarded;
// HEAD/metadata checks never download object content. Both must be forbidden.
var errCloudReadable = errors.New("cloud credential grants read or list access; recreate a restricted upload identity before reconnecting")

func (u *cloudUploader) checkRestricted(ctx context.Context, probe string) error {
	denied := func(status int) error {
		if status >= 200 && status < 300 {
			return errCloudReadable
		}
		if status != 403 {
			return errors.New("cloud access restrictions could not be verified; read and list must both be denied by provider permissions")
		}
		return nil
	}
	if u.s3 != nil {
		_, err := u.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(u.dest.plan.Bucket), Key: &probe}, func(o *s3.Options) { o.RetryMaxAttempts = 1 })
		if err == nil {
			return errCloudReadable
		}
		var response interface{ HTTPStatusCode() int }
		if !errors.As(err, &response) {
			return errors.New("AWS object-read denial could not be checked")
		}
		if e := denied(response.HTTPStatusCode()); e != nil {
			return e
		}
		_, err = u.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(u.dest.plan.Bucket), MaxKeys: aws.Int32(0)}, func(o *s3.Options) { o.RetryMaxAttempts = 1 })
		if err == nil {
			return errCloudReadable
		}
		if !errors.As(err, &response) {
			return errors.New("AWS object-list denial could not be checked")
		}
		if e := denied(response.HTTPStatusCode()); e != nil {
			return e
		}
		var api smithy.APIError
		if !errors.As(err, &api) || (api.ErrorCode() != "AccessDenied" && api.ErrorCode() != "Forbidden") {
			return errors.New("AWS refused the list check without proving permission denial")
		}

		return nil
	}
	var targets []string
	client := cloudHTTP
	method := "HEAD"
	if u.google != nil {
		client = u.google
		method = "GET"
		base := "https://storage.googleapis.com/storage/v1/b/" + url.PathEscape(u.dest.plan.Bucket) + "/o"
		targets = []string{base + "/" + url.PathEscape(probe) + "?fields=name", base + "?maxResults=1&fields=kind"}
	} else {
		targets = []string{u.azureURL(probe, nil), u.azureURL("", url.Values{"restype": {"container"}, "comp": {"list"}, "maxresults": {"1"}})}
	}
	for i, target := range targets {
		if i == 1 {
			method = "GET"
		}
		req, _ := http.NewRequestWithContext(ctx, method, target, nil)
		req.Header.Set("x-ms-version", "2023-11-03")
		response, err := cloudResponse(client, req)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		if readErr != nil {
			return errors.New("cloud permission check response was interrupted")
		}
		if err = denied(response.StatusCode); err != nil {
			return err
		}
		if u.google != nil {
			var detail struct {
				Error struct {
					Status string `json:"status"`
					Errors []struct {
						Reason string `json:"reason"`
					} `json:"errors"`
				} `json:"error"`
			}
			permissionDenied := false
			if json.Unmarshal(data, &detail) == nil {
				permissionDenied = detail.Error.Status == "PERMISSION_DENIED"
				for _, entry := range detail.Error.Errors {
					if entry.Reason == "forbidden" {
						permissionDenied = true
					}
				}
			}
			if !permissionDenied {
				return errors.New("Google refused the check without proving permission denial; check quota, authentication and IAM")
			}
		} else if response.Header.Get("x-ms-error-code") != "AuthorizationPermissionMismatch" {
			return errors.New("Azure refused the check without proving permission denial; check SAS permissions and expiry")
		}

	}
	return nil
}
