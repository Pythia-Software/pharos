//go:build !darwin && !linux

package archive

import "os"

// Capture export needs no-follow directory-relative opens, flock and an
// exclusive rename, which only the Darwin and Linux adapters provide.
const captureExportSupported = false

func exportOpenRoot(string) (*exportDir, error) { return nil, errCaptureExportUnsupported }
func (d *exportDir) Close() error               { return nil }
func (d *exportDir) sub(string) (*exportDir, error) {
	return nil, errCaptureExportUnsupported
}
func (d *exportDir) stat(string) (exportStat, bool, error) {
	return exportStat{}, false, errCaptureExportUnsupported
}
func exportStatPath(string) (exportStat, error)   { return exportStat{}, errCaptureExportUnsupported }
func (d *exportDir) self() (exportStat, error)    { return exportStat{}, errCaptureExportUnsupported }
func (d *exportDir) list() (*exportLister, error) { return nil, errCaptureExportUnsupported }
func (d *exportDir) openRead(string) (*os.File, exportStat, error) {
	return nil, exportStat{}, errCaptureExportUnsupported
}
func (d *exportDir) openLock(string, string) (*os.File, error) {
	return nil, errCaptureExportUnsupported
}
func fstatExport(*os.File) (exportStat, error)       { return exportStat{}, errCaptureExportUnsupported }
func (d *exportDir) mkdir(string) error              { return errCaptureExportUnsupported }
func (d *exportDir) create(string) (*os.File, error) { return nil, errCaptureExportUnsupported }
func (d *exportDir) unlink(string, bool) error       { return errCaptureExportUnsupported }
func (d *exportDir) renameNoReplace(string, *exportDir, string) error {
	return errCaptureExportUnsupported
}
