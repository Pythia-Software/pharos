//go:build !unix

package main

import "os"

// reserveStdout returns stdout. On Windows a native library's writes to
// stdout would reach Pharos; redirecting them is left to a Windows port.
func reserveStdout() *os.File { return os.Stdout }
