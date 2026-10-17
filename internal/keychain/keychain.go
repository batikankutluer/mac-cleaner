// Package keychain keeps the TypeSafe API key in the macOS login keychain via
// /usr/bin/security. The key is written through stdin, never as an argument,
// so it never shows up in the process list.
package keychain

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

const account = "typesafe"

// Service is the keychain item's name; tests use their own.
var Service = "mac-cleaner"

// validKey also guarantees the key can't break out of the security -i command line.
var validKey = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]{8,512}$`)

// Get returns the stored key, or "" when there is none.
func Get() (string, error) {
	out, err := exec.Command("security", "find-generic-password", "-s", Service, "-a", account, "-w").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 44 { // errSecItemNotFound
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("anahtar zinciri okunamadı: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func Set(key string) error {
	key = strings.TrimSpace(key)
	if !validKey.MatchString(key) {
		return errors.New("bu bir API anahtarına benzemiyor")
	}
	cmd := exec.Command("security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %s -a %s -w %s\n", Service, account, key))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("anahtar zincirine yazılamadı: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func Delete() error {
	err := exec.Command("security", "delete-generic-password", "-s", Service, "-a", account).Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 44 {
		return nil
	}
	return err
}
