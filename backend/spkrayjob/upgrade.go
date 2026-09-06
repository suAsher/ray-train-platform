package spkrayjob

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"ray-train-platform-backend/domain"
)

func runUpgrade(ctx context.Context, arguments []string, stdout, stderr io.Writer) error {
	if !selfUpgradeReleased(runtime.GOOS) {
		return errors.New("此平台尚未开放自更新；请在平台 UI 的集群外提交页重新下载安装，Windows 自更新待实机验收后开放")
	}
	set := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	configPath := set.String("config", "", "saved login configuration")
	caFile := set.String("ca-file", "", "private CA PEM file")
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 {
		return errors.New("usage: spk-rayjob upgrade [--config path] [--ca-file path]")
	}
	if _, err := domain.CompareCLIReleaseVersions(Version, Version); err != nil {
		return errors.New("开发或未知版本不支持自动升级；请手动下载正式版本")
	}
	config, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	server, transport, err := openPlatformConnection(config.Server, *caFile, nil)
	if err != nil {
		return fmt.Errorf("upgrade requires saved HTTPS login configuration: %w", err)
	}
	client := &Client{server: server, httpClient: transport}
	manifest, err := client.releaseManifest(ctx)
	if err != nil {
		return err
	}
	cmp, _ := domain.CompareCLIReleaseVersions(Version, manifest.LatestVersion)
	if cmp >= 0 {
		_, err := fmt.Fprintln(stdout, "已是最新版本："+Version)
		return err
	}
	filename := artifactFilename(runtime.GOOS, runtime.GOARCH)
	var artifact ReleaseArtifact
	for _, candidate := range manifest.Artifacts {
		if candidate.Filename == filename {
			artifact = candidate
		}
	}
	if artifact.Filename == "" {
		return errors.New("当前平台没有可用升级文件；请手动下载")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	target, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	if err := client.installRelease(ctx, artifact, target); err != nil {
		return fmt.Errorf("升级失败，原程序已保留：%w", err)
	}
	if runtime.GOOS == "windows" {
		_, err = fmt.Fprintln(stdout, "已校验升级文件；退出后完成替换，旧版本保留为 .previous；请重新运行 spk-rayjob version 确认。")
	} else {
		_, err = fmt.Fprintln(stdout, "升级成功："+manifest.LatestVersion)
	}
	return err
}

func selfUpgradeReleased(system string) bool { return system == "linux" || system == "darwin" }

func (client *Client) installRelease(ctx context.Context, artifact ReleaseArtifact, target string) error {
	if err := checkUpgradeBackup(target, runtime.GOOS); err != nil {
		return err
	}
	if err := (ReleaseManifest{SchemaVersion: 1, LatestVersion: "release-20000101-01", Artifacts: []ReleaseArtifact{artifact}}).Validate(); err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("upgrade target is not a regular file")
	}
	lock := target + ".upgrade-lock"
	if err := os.Mkdir(lock, 0700); err != nil {
		return fmt.Errorf("cannot acquire upgrade lock (another upgrade or stale lock): %w", err)
	}
	keepFiles := false
	defer func() {
		if !keepFiles {
			os.Remove(lock)
		}
	}()
	temporary, err := os.CreateTemp(filepath.Dir(target), ".spk-rayjob-upgrade-*")
	if err != nil {
		return err
	}
	staged := temporary.Name()
	defer func() {
		temporary.Close()
		if !keepFiles {
			os.Remove(staged)
		}
	}()
	response, err := client.releaseGET(ctx, artifact.Filename)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.ContentLength >= 0 && response.ContentLength != artifact.Size {
		return errors.New("release artifact size mismatch")
	}
	digest := sha256.New()
	count, err := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(response.Body, artifact.Size+1))
	if err != nil {
		return err
	}
	if count != artifact.Size || hex.EncodeToString(digest.Sum(nil)) != artifact.SHA256 {
		return errors.New("release artifact checksum or size mismatch")
	}
	if err := verifyReleaseArtifactBinary(staged, artifact); err != nil {
		return err
	}
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	deferred, err := replaceExecutable(staged, target, lock)
	keepFiles = deferred && err == nil
	return err
}

func checkUpgradeBackup(target, system string) error {
	if system != "windows" {
		return nil
	}
	if _, err := os.Lstat(target + ".previous"); err == nil {
		return errors.New("已有 .previous 回滚文件；请确认当前版本可用，将该备份移到安全位置后重新升级")
	} else if !os.IsNotExist(err) {
		return err
	}
	return nil
}

func verifyReleaseArtifactBinary(path string, artifact ReleaseArtifact) error {
	invalid := fmt.Errorf("release artifact is not a %s/%s executable", artifact.OS, artifact.Arch)
	switch artifact.OS + "/" + artifact.Arch {
	case "linux/amd64":
		f, err := elf.Open(path)
		if err != nil {
			return invalid
		}
		defer f.Close()
		if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != elf.EM_X86_64 || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) || f.Entry == 0 || len(f.Progs) == 0 {
			return invalid
		}
	case "darwin/arm64":
		f, err := macho.Open(path)
		if err != nil {
			return invalid
		}
		defer f.Close()
		if f.Magic != macho.Magic64 || f.Cpu != macho.CpuArm64 || f.Type != macho.TypeExec || len(f.Loads) == 0 {
			return invalid
		}
	case "windows/amd64":
		f, err := pe.Open(path)
		if err != nil {
			return invalid
		}
		defer f.Close()
		h, ok := f.OptionalHeader.(*pe.OptionalHeader64)
		if !ok || h.AddressOfEntryPoint == 0 || f.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || f.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 || f.Characteristics&pe.IMAGE_FILE_DLL != 0 || len(f.Sections) == 0 {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}
