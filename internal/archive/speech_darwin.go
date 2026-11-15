//go:build darwin

package archive

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

const speechSupported = true

// speechNative has the system voice read a text file into an AAC file. The
// text is passed as a file, never as arguments, and say's output is not
// reported: only whether it wrote the recording.
func speechNative(ctx context.Context, textFile, audioFile string) error {
	if err := exec.CommandContext(ctx, "/usr/bin/say", "-f", textFile, "-o", audioFile, "--data-format=aac").Run(); err != nil {
		return fmt.Errorf("say: %v", err)
	}
	if info, err := os.Stat(audioFile); err != nil || info.Size() == 0 {
		return fmt.Errorf("say wrote no recording")
	}
	return nil
}
