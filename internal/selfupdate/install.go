package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func equalDigest(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }

func resolveTarget(target string) (string, error) {
	var err error
	if target == "" {
		target, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("cannot locate the running executable")
		}
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("invalid update target path")
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("update target must be an existing executable")
	}
	if _, err := targetInfo(target); err != nil {
		return "", err
	}
	return target, nil
}

func targetInfo(target string) (os.FileInfo, error) {
	info, err := os.Lstat(target)
	if err != nil {
		return nil, fmt.Errorf("cannot inspect update target")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, fmt.Errorf("update target must be a regular executable file")
	}
	return info, nil
}

func acquireLock(target string) (func(), error) {
	path := target + ".update.lock"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot acquire update lock %s; another updater may be running (inspect a stale lock before removing it)", path)
	}
	_, writeErr := fmt.Fprintf(f, "pid=%d\ncreated=%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(path)
		return nil, fmt.Errorf("cannot write update lock")
	}
	return func() { _ = os.Remove(path) }, nil
}

func install(ctx context.Context, api *github, target string, binary, manifest asset, p platform) error {
	original, err := targetInfo(target)
	if err != nil {
		return err
	}
	previous := target + ".previous"
	if info, err := os.Lstat(previous); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("backup path must not be a symlink or nonregular file")
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect previous binary backup")
	}
	response, err := api.get(ctx, manifest.URL, "application/octet-stream")
	if err != nil {
		return err
	}
	raw, readErr := readBounded(response.Body, maxManifest)
	response.Body.Close()
	if readErr != nil {
		return fmt.Errorf("checksum manifest exceeds size limit or could not be read")
	}
	if int64(len(raw)) != manifest.Size {
		return fmt.Errorf("checksum manifest size does not match release metadata")
	}
	if err := verifyDigest(manifest, raw); err != nil {
		return err
	}
	expected, err := manifestChecksum(raw, binary.Name)
	if err != nil {
		return err
	}
	if binary.Digest != "" {
		apiDigest, err := digest(binary.Digest)
		if err != nil {
			return err
		}
		if !equalDigest(expected, apiDigest) {
			return fmt.Errorf("binary API digest disagrees with SHA256SUMS")
		}
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".proxy-update-*")
	if err != nil {
		return fmt.Errorf("cannot create update file in executable directory (check permissions)")
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return fmt.Errorf("cannot secure temporary binary")
	}
	response, err = api.get(ctx, binary.URL, "application/octet-stream")
	if err != nil {
		return err
	}
	hasher := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(file, hasher), io.LimitReader(response.Body, maxBinary+1))
	response.Body.Close()
	if copyErr != nil {
		return fmt.Errorf("binary download or temporary file write failed")
	}
	if n > maxBinary {
		return fmt.Errorf("binary download exceeds size limit")
	}
	if n != binary.Size {
		return fmt.Errorf("binary size does not match release metadata")
	}
	if !equalDigest(expected, hasher.Sum(nil)) {
		return fmt.Errorf("binary SHA256 verification failed; installed binary was not changed")
	}
	if err := validateELF(file, p); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Copy only ordinary rwx bits and remove group/other write permissions.
	// Never inherit setuid, setgid or sticky bits from the old executable.
	mode := original.Mode().Perm() & 0755
	if err := file.Chmod(mode); err != nil {
		return fmt.Errorf("cannot set binary permissions")
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("cannot sync downloaded binary")
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("cannot close downloaded binary")
	}
	if err := unchangedTarget(target, original); err != nil {
		return err
	}
	if err := backup(target, previous, original, mode); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := unchangedTarget(target, original); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), target); err != nil {
		return fmt.Errorf("cannot atomically replace installed binary; original is unchanged")
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return fmt.Errorf("binary installed but directory sync failed; inspect target and .previous before retrying")
	}
	return nil
}

func manifestChecksum(raw []byte, name string) ([]byte, error) {
	checksums := make(map[string][]byte)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return nil, fmt.Errorf("invalid SHA256SUMS format")
		}
		filename := strings.TrimPrefix(fields[1], "*")
		if !assetNamePattern.MatchString(filename) || checksums[filename] != nil {
			return nil, fmt.Errorf("invalid or duplicate filename in SHA256SUMS")
		}
		hash, err := hex.DecodeString(fields[0])
		if err != nil {
			return nil, fmt.Errorf("invalid SHA256 in SHA256SUMS")
		}
		checksums[filename] = hash
	}
	if scanner.Err() != nil {
		return nil, fmt.Errorf("invalid SHA256SUMS format")
	}
	if checksums[name] == nil {
		return nil, fmt.Errorf("SHA256SUMS does not contain the selected binary")
	}
	return checksums[name], nil
}

func validateELF(file *os.File, p platform) error {
	binary, err := elf.NewFile(file)
	if err != nil {
		return fmt.Errorf("downloaded asset is not a valid Linux ELF executable")
	}
	wantMachine := elf.EM_NONE
	wantClass := elf.ELFCLASS64
	switch p.arch {
	case "amd64":
		wantMachine = elf.EM_X86_64
	case "arm64":
		wantMachine = elf.EM_AARCH64
	case "arm":
		wantMachine = elf.EM_ARM
		wantClass = elf.ELFCLASS32
	}
	if binary.Machine != wantMachine || binary.Class != wantClass || binary.Data != elf.ELFDATA2LSB || (binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN) {
		return fmt.Errorf("downloaded ELF architecture/type does not match this server")
	}
	return nil
}

func unchangedTarget(target string, original os.FileInfo) error {
	current, err := targetInfo(target)
	if err != nil {
		return err
	}
	if !os.SameFile(original, current) || current.Size() != original.Size() || !current.ModTime().Equal(original.ModTime()) {
		return fmt.Errorf("installed binary changed during update; refusing to overwrite it")
	}
	return nil
}

func backup(target, previous string, original os.FileInfo, mode os.FileMode) error {
	source, err := os.Open(target)
	if err != nil {
		return fmt.Errorf("cannot read installed binary for backup")
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !os.SameFile(original, info) || !info.Mode().IsRegular() {
		return fmt.Errorf("update target changed before backup")
	}
	destination, err := os.CreateTemp(filepath.Dir(target), ".proxy-backup-*")
	if err != nil {
		return fmt.Errorf("cannot create previous binary backup")
	}
	defer os.Remove(destination.Name())
	defer destination.Close()
	if n, err := io.Copy(destination, io.LimitReader(source, original.Size()+1)); err != nil || n != original.Size() {
		return fmt.Errorf("cannot copy previous binary backup")
	}
	if err := destination.Chmod(mode); err != nil {
		return fmt.Errorf("cannot set previous binary permissions")
	}
	if err := destination.Sync(); err != nil {
		return fmt.Errorf("cannot sync previous binary backup")
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("cannot close previous binary backup")
	}
	if err := os.Rename(destination.Name(), previous); err != nil {
		return fmt.Errorf("cannot atomically save previous binary backup")
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return fmt.Errorf("cannot sync previous binary backup directory")
	}
	return nil
}

func syncDirectory(directory string) error {
	f, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
