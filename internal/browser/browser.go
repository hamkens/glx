// Package browser opens URLs in the user's default web browser.
package browser

import (
	"fmt"
	"os/exec"
	"runtime"
)

// Open launches the default browser for url. It returns an error if no opener
// is available or the command fails to start (it does not wait for the
// browser to exit).
func Open(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default: // linux, *bsd
		cmd = "xdg-open"
	}
	if _, err := exec.LookPath(cmd); err != nil {
		return fmt.Errorf("no browser opener (%s) found on PATH", cmd)
	}
	return exec.Command(cmd, append(args, url)...).Start()
}
