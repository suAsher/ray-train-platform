package helpdocs

import (
	"ray-train-platform-backend/domain"
	"strings"
	"testing"
)

func TestEnvironmentImageGuidePreservesUserQuestionsAndBoundaries(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID: "custom-environment", UpdatedBy: PlatformSeedActor})
	for _, text := range []string{"保存训练环境", "CLI Secret", "集群 CPU", "固定摘要", "READY", "原有 Base", "不会把整个容器", "停止或重建", "Harbor 仓库自身"} {
		if !strings.Contains(article.Markdown, text) {
			t.Errorf("missing user guidance %q", text)
		}
	}
	manual := ProjectHelpArticle(domain.HelpDocument{ID: "custom-environment", UpdatedBy: "user", Markdown: "团队自己维护的说明"})
	if !strings.HasPrefix(manual.Markdown, "团队自己维护的说明") {
		t.Fatal("overwrote manually published guidance")
	}
}

func TestEnvironmentImageGuideExplainsHomeAndManagedPython(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID: "custom-environment", UpdatedBy: PlatformSeedActor})
	for _, text := range []string{
		"在 ~/ 执行安装，能保存吗？",
		"当前目录不决定安装位置",
		"python -c \"import sys; print(sys.executable); print(sys.prefix)\"",
		"python -m pip --version",
		"/opt/raytrain/environment/bin/python",
		"python -m pip install --only-binary=:all: -r requirements.txt",
		"python -m pip check",
		"RayTrain Environment",
		"~/anaconda3/envs/",
		"原 Base 自带的 /home/ray/anaconda3",
		"docker commit",
	} {
		if !strings.Contains(article.Markdown, text) {
			t.Errorf("missing installation or capture boundary %q", text)
		}
	}
	for _, misleading := range []string{"系统软件、Conda 环境、可编辑安装、未提供可重建材料的本地包或手动修改依赖文件，会明确拒绝保存", "~/ 中的内容都会保存"} {
		if strings.Contains(article.Markdown, misleading) {
			t.Errorf("overpromises capture detection or persistence: %q", misleading)
		}
	}
}

func TestEnvironmentImageGuideSeparatesImageCaptureFromStoragePersistence(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID: "custom-environment", UpdatedBy: PlatformSeedActor})
	if !strings.Contains(article.Markdown, "| 内容或位置 | 会进入保存的训练镜像吗？ | 停止或重建调试环境后 |") {
		t.Fatal("missing separate image-capture and storage-persistence columns")
	}
	for _, row := range []struct {
		location string
		image    string
		storage  string
	}{
		{"/opt/raytrain/environment 中新增的 wheel 依赖", "可以", "READY"},
		{"~/venv、~/conda、~/.local 等自建环境", "不进入", "临时"},
		{"普通 home（通常为 /home/ray）中的代码、配置和文件", "不进入", "临时"},
		{"/workspace 中的项目和文件", "不进入", "持久"},
		{"/mnt/storage/me 中的数据、权重和结果", "不进入", "持久"},
		{"APT/系统软件与容器其他目录", "不进入", "重建后不会保留"},
	} {
		found := false
		for _, line := range strings.Split(article.Markdown, "\n") {
			cells := strings.Split(line, "|")
			if len(cells) != 5 || strings.TrimSpace(cells[1]) != row.location {
				continue
			}
			found = true
			if !strings.Contains(cells[2], row.image) || !strings.Contains(cells[3], row.storage) {
				t.Errorf("capture/persistence boundary is unclear for %s: %s", row.location, line)
			}
		}
		if !found {
			t.Errorf("missing storage boundary for %s", row.location)
		}
	}
}

func TestDebugGuidesExplainTemporarySudoSystemPackages(t *testing.T) {
	documents, err := Documents()
	if err != nil {
		t.Fatal(err)
	}
	guides := map[string]string{
		"saved environment": ProjectHelpArticle(domain.HelpDocument{ID: "custom-environment", UpdatedBy: PlatformSeedActor}).Markdown,
	}
	for _, document := range documents {
		if document.ID == "debug" {
			guides["debug seed"] = document.Markdown
			document.UpdatedBy = PlatformSeedActor
			guides["debug article"] = ProjectHelpArticle(document).Markdown
		}
	}
	for _, guide := range PublicGuides() {
		if guide.ID == "debug" {
			guides["public debug"] = guide.Markdown
		}
	}
	if len(guides) != 4 {
		t.Fatalf("missing debug help entry: got %d", len(guides))
	}
	for name, markdown := range guides {
		t.Run(name, func(t *testing.T) {
			for _, required := range []string{
				"升级后", "新建", "sudo apt-get update && sudo apt-get install",
				"旧镜像", "sudo: command not found",
				"系统包不会进入保存的训练环境", "重建后不会保留",
				"/workspace", "个人持久", "root 所有者",
				"/opt/raytrain/environment/bin/python",
			} {
				if !strings.Contains(markdown, required) {
					t.Errorf("missing system package boundary %q", required)
				}
			}
			for _, forbidden := range []string{"apt install 不可用", "apt 不可用", "整个容器都会保存", "系统包会随保存的训练环境一起保留"} {
				if strings.Contains(markdown, forbidden) {
					t.Errorf("stale or misleading system package guidance %q", forbidden)
				}
			}
		})
	}
}

func TestEnvironmentImageGuideOrdersSaveAndTrainWorkflow(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID: "custom-environment", UpdatedBy: PlatformSeedActor})
	previous := -1
	for _, step := range []string{
		"### 1. 选择支持保存的 Base 调试镜像",
		"### 2. 确认 Python，再安装并测试",
		"### 3. 保存到自己有写权限的 Harbor 项目",
		"### 4. 等待 READY，再用于训练",
		"### 保存后，浏览器怎样提交训练",
		"### 同一环境怎样通过命令行提交",
	} {
		index := strings.Index(article.Markdown, step)
		if index <= previous {
			t.Fatalf("missing or out-of-order workflow step %q", step)
		}
		previous = index
	}
	for _, text := range []string{
		"harbor.wellspiking.ai | 个人资料中的 CLI Secret",
		"harbor.qomolo.com | 账号密码",
		"没有固定到某个人的项目",
		"仅本人", "当前团队", "Harbor 仓库自身",
		"验证目标写权限", "构建并推送", "固定摘要",
		"不包含训练代码", "PLATFORM_DATASET_PATH", "PLATFORM_OUTPUT_PATH",
		"--dir .", "--image '<所选仓库>/<项目>/<镜像>@sha256:",
		"GPU 兼容性",
	} {
		if !strings.Contains(article.Markdown, text) {
			t.Errorf("missing save/train workflow guidance %q", text)
		}
	}
}

func TestEnvironmentImageGuidePreservesPublishedManualDockerInstructions(t *testing.T) {
	const manual = "### 高级：手工 Docker 构建\n\n~~~dockerfile\nFROM approved-base\nRUN apt-get update\n~~~\n\n团队自己的镜像登记说明。"
	article := ProjectHelpArticle(domain.HelpDocument{ID: "custom-environment", UpdatedBy: "user", Markdown: manual})
	if !strings.HasPrefix(article.Markdown, manual) {
		t.Fatal("manually published Docker guide must remain intact")
	}
	if strings.Contains(article.Markdown, "### 1. 选择支持保存的 Base 调试镜像") {
		t.Fatal("platform seed projection must not replace a manually published guide")
	}
}

func TestEnvironmentImageGuideSearchMetadataCoversSavingAndHome(t *testing.T) {
	article := ProjectHelpArticle(domain.HelpDocument{ID: "custom-environment", UpdatedBy: PlatformSeedActor})
	for _, text := range []string{"调试", "安装", "保存镜像", "浏览器", "CLI", "目录"} {
		if !strings.Contains(article.Summary, text) {
			t.Errorf("summary does not explain environment workflow: missing %q", text)
		}
	}
	keywords := strings.Join(article.Keywords, " ")
	for _, text := range []string{"保存", "commit", "home", "Harbor", "Conda"} {
		if !strings.Contains(keywords, text) {
			t.Errorf("environment guide is not searchable by %q", text)
		}
	}
}
