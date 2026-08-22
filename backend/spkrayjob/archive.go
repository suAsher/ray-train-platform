package spkrayjob

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const MaxLogicalArchiveSize int64 = 2 << 30

var zipEpoch = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

type Archive struct {
	Path      string
	SHA256    string
	SizeBytes int64
}

type archiveFile struct {
	path string
	name string
	size int64
}

// BuildArchive creates a deterministic temporary ZIP for a local source tree.
func BuildArchive(root string) (Archive, error) {
	files, err := collectArchiveFiles(root)
	if err != nil {
		return Archive{}, err
	}
	output, err := os.CreateTemp("", "spk-rayjob-*.zip")
	if err != nil {
		return Archive{}, fmt.Errorf("create archive: %w", err)
	}
	outputPath := output.Name()
	if err := output.Chmod(0o600); err != nil {
		_ = output.Close()
		_ = os.Remove(outputPath)
		return Archive{}, fmt.Errorf("protect archive: %w", err)
	}
	hash := sha256.New()
	writer := zip.NewWriter(io.MultiWriter(output, hash))
	for _, file := range files {
		header := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		header.SetModTime(zipEpoch)
		header.SetMode(0o644)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			_ = output.Close()
			_ = os.Remove(outputPath)
			return Archive{}, fmt.Errorf("create zip entry: %w", err)
		}
		input, err := os.Open(file.path)
		if err != nil {
			_ = writer.Close()
			_ = output.Close()
			_ = os.Remove(outputPath)
			return Archive{}, fmt.Errorf("open source file: %w", err)
		}
		_, copyErr := io.Copy(entry, input)
		closeErr := input.Close()
		if copyErr != nil || closeErr != nil {
			_ = writer.Close()
			_ = output.Close()
			_ = os.Remove(outputPath)
			return Archive{}, fmt.Errorf("read source file")
		}
	}
	if err := writer.Close(); err != nil {
		_ = output.Close()
		_ = os.Remove(outputPath)
		return Archive{}, fmt.Errorf("finish archive: %w", err)
	}
	info, err := output.Stat()
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(outputPath)
		return Archive{}, fmt.Errorf("inspect archive: %w", err)
	}
	return Archive{Path: outputPath, SHA256: hex.EncodeToString(hash.Sum(nil)), SizeBytes: info.Size()}, nil
}

func collectArchiveFiles(root string) ([]archiveFile, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve source directory: %w", err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve source directory: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("source directory is unavailable")
	}
	ignores, err := loadIgnoreRules(resolvedRoot)
	if err != nil {
		return nil, err
	}
	files := make([]archiveFile, 0)
	var logicalSize int64
	err = filepath.WalkDir(resolvedRoot, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == resolvedRoot {
			return nil
		}
		relative, err := filepath.Rel(resolvedRoot, current)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if name == ".git" || strings.HasPrefix(name, ".git/") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if ignores.matches(name, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if err := validateArchiveSymlink(resolvedRoot, current); err != nil {
				return err
			}
			linkedInfo, err := os.Stat(current)
			if err != nil || linkedInfo.IsDir() {
				return fmt.Errorf("source symlink is unsupported")
			}
			return appendArchiveFile(&files, &logicalSize, archiveFile{path: current, name: name, size: linkedInfo.Size()})
		}
		if entry.IsDir() {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil || !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("source entry is not a regular file")
		}
		return appendArchiveFile(&files, &logicalSize, archiveFile{path: current, name: name, size: entryInfo.Size()})
	})
	if err != nil {
		return nil, fmt.Errorf("walk source directory: %w", err)
	}
	sort.Slice(files, func(left, right int) bool { return files[left].name < files[right].name })
	return files, nil
}

func appendArchiveFile(files *[]archiveFile, total *int64, file archiveFile) error {
	if file.size < 0 || file.size > MaxLogicalArchiveSize-*total {
		return fmt.Errorf("source content exceeds 2 GiB logical limit")
	}
	*total += file.size
	*files = append(*files, file)
	return nil
}

func validateArchiveSymlink(root, value string) error {
	resolved, err := filepath.EvalSymlinks(value)
	if err != nil {
		return fmt.Errorf("resolve source symlink: %w", err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("source symlink escapes source directory")
	}
	return nil
}

type ignoreRules []ignoreRule

type ignoreRule struct {
	pattern   string
	negate    bool
	directory bool
	anchored  bool
}

func loadIgnoreRules(root string) (ignoreRules, error) {
	files := []string{filepath.Join(root, ".gitignore"), filepath.Join(root, ".rayignore")}
	rules := make(ignoreRules, 0)
	for _, file := range files {
		contents, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read ignore file: %w", err)
		}
		for _, line := range strings.Split(string(contents), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			rule := ignoreRule{negate: strings.HasPrefix(line, "!")}
			line = strings.TrimPrefix(line, "!")
			rule.anchored = strings.HasPrefix(line, "/")
			line = strings.TrimPrefix(line, "/")
			rule.directory = strings.HasSuffix(line, "/")
			rule.pattern = strings.TrimSuffix(line, "/")
			if rule.pattern != "" {
				rules = append(rules, rule)
			}
		}
	}
	return rules, nil
}

func (rules ignoreRules) matches(name string, directory bool) bool {
	ignored := false
	for _, rule := range rules {
		if rule.directory && !directory && !strings.HasPrefix(name, rule.pattern+"/") {
			continue
		}
		if ignorePatternMatches(rule.pattern, name, rule.anchored) {
			ignored = !rule.negate
		}
	}
	return ignored
}

func ignorePatternMatches(pattern, name string, anchored bool) bool {
	if matched, _ := path.Match(pattern, name); matched {
		return true
	}
	if anchored && !strings.Contains(pattern, "/") {
		rootName := strings.SplitN(name, "/", 2)[0]
		matched, _ := path.Match(pattern, rootName)
		return matched
	}
	if !anchored && !strings.Contains(pattern, "/") {
		for _, part := range strings.Split(name, "/") {
			if matched, _ := path.Match(pattern, part); matched {
				return true
			}
		}
	}
	return name == pattern || strings.HasPrefix(name, pattern+"/")
}
