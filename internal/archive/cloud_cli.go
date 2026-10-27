package archive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/term"
)

func runCloudCLI(config Config, args []string) error {
	if len(args) != 3 || args[0] != "verify" {
		return errors.New("usage: pharos [--config PATH] cloud-backup verify RUN_ID DOWNLOADED_ARCHIVE")
	}
	if _, err := os.Stat(config.CatalogPath); err != nil {
		return errors.New("the library catalog is unavailable")
	}
	catalog, err := openCatalog(config.CatalogPath, func(*Catalog) error { return nil })
	if err != nil {
		return err
	}
	defer catalog.Close()
	// It saves the verification state, so it is a catalog writer: a lease
	// beside the catalog, and a journal row (§6.5).
	return runCLIOperation(config, catalog, "cloud-backup", OperationSpec{Kind: "cloud.verify", Legacy: args[1]}, func(context.Context, *Op) error {
		return verifyCloudCLI(config, catalog, args)
	})
}

func verifyCloudCLI(config Config, catalog *Catalog, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run, err := catalog.cloudRun(ctx, args[1])
	if err != nil || run.State != "complete" {
		return errors.New("no completed upload with that run ID")
	}
	dir := expandPath(config.StagingRoot)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	d, err := catalog.cloudDestination(ctx, run.DestinationID, false)
	if err != nil {
		return err
	}
	var password []byte
	if d.plan.PasswordProtected {
		tty, e := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if e != nil {
			return errors.New("open an interactive Terminal to enter the recovery password")
		}
		defer tty.Close()
		if !term.IsTerminal(int(tty.Fd())) {
			return errors.New("password verification requires a controlling terminal")
		}
		fmt.Fprint(tty, "Recovery password for this downloaded archive: ")
		password, e = term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		if e != nil {
			return e
		}
		defer clear(password)
	}
	fmt.Println("Checking the downloaded revision and validating every recovered file…")
	if err = catalog.finishCloudVerification(ctx, config, &run, args[2], password); err != nil {
		return err
	}
	fmt.Printf("Verified %d files (%s). Recovered copy: %s\nReturn to Pharos to continue. The active library was not changed.\n", run.Files, humanBytes(run.FileBytes), run.RestoredDirectory)
	return nil
}

// cloud-restore works on a new Mac without the old Pharos catalog or config.
func runCloudRestoreCLI(args []string) error {
	if len(args) != 2 {
		return errors.New("usage: pharos cloud-restore DOWNLOADED_ARCHIVE NEW_OUTPUT_FOLDER")
	}
	archive, err := os.OpenFile(expandPath(args[0]), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("archive must be a regular local file")
	}
	root := expandPath(args[1])
	if !filepath.IsAbs(root) {
		return errors.New("use an absolute recovery folder or ~/")
	}
	if _, err = os.Stat(filepath.Dir(root)); err != nil {
		return errors.New("the output folder's parent must already exist")
	}
	if err = os.Mkdir(root, 0700); err != nil {
		return errors.New("choose a new output folder that does not already exist")
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(root)
		}
	}()
	prefix := make([]byte, len(cloudMagic))
	n, err := archive.ReadAt(prefix, 0)
	if err != nil || n != len(prefix) {
		return errors.New("invalid archive")
	}
	var password []byte
	if string(prefix) == cloudMagic {
		tty, e := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if e != nil {
			return errors.New("open an interactive Terminal to enter the recovery password")
		}
		defer tty.Close()
		if !term.IsTerminal(int(tty.Fd())) {
			return errors.New("recovery requires a controlling terminal")
		}
		fmt.Fprint(tty, "Recovery password: ")
		password, e = term.ReadPassword(int(tty.Fd()))
		fmt.Fprintln(tty)
		if e != nil {
			return e
		}
		defer clear(password)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	payload, err := cloudPayload(archive, password, "")
	if err != nil {
		return err
	}
	directory, err := extractCloudArchive(ctx, Config{StagingRoot: root}, payload, nil)
	if err != nil {
		return err
	}
	complete = true
	fmt.Printf("Recovered and validated the archive in %s\nNo active library was opened or replaced.\n", directory)
	return nil
}
