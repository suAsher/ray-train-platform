package spkrayjob

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// runImages only reads the authenticated catalogue. It never pulls or executes
// images, and administrator declarations are not presented as attestations.
func runImages(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	set := flag.NewFlagSet("images", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	var format outputFormatFlag
	bindOutputFormatFlag(set, &format)
	if err := set.Parse(arguments); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return errors.New("images does not accept positional arguments")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	images, err := client.TrainingImages(ctx)
	if err != nil {
		return err
	}
	if format.json {
		return writeJSON(stdout, images)
	}
	return renderImages(stdout, images)
}

func renderImages(stdout io.Writer, images []catalogImage) error {
	if len(images) == 0 {
		_, err := fmt.Fprintln(stdout, "暂无可用训练镜像，请联系团队管理员登记。")
		return err
	}
	var out strings.Builder
	out.WriteString("训练镜像环境（管理员声明，不代表平台已验证；支持 tag 或 digest）\n")
	for _, image := range images {
		marker := ""
		if image.IsDefault {
			marker = " [默认]"
		}
		fmt.Fprintf(&out, "\n%s%s\n  镜像: %s\n  Ray: %s\n", catalogText(image.Name), marker, catalogText(image.Reference), catalogText(image.RayVersion))
		engines := make([]string, 0, len(image.SupportedEngines))
		for _, engine := range image.SupportedEngines {
			engines = append(engines, catalogText(string(engine)))
		}
		fmt.Fprintf(&out, "  训练引擎: %s\n", strings.Join(engines, ", "))
		for _, field := range []struct{ label, value string }{
			{"说明", image.Description}, {"框架", image.Framework}, {"Python", image.Environment.Python},
			{"CUDA", image.Environment.CUDA}, {"PyTorch", image.Environment.PyTorch}, {"MLflow", image.Environment.MLflow},
			{"依赖", image.Environment.Dependencies}, {"适用场景", image.Environment.UseCases}, {"管理员验证备注", image.Environment.ValidationNotes},
		} {
			if field.value != "" {
				fmt.Fprintf(&out, "  %s: %s\n", field.label, catalogText(field.value))
			}
		}
	}
	_, err := io.WriteString(stdout, out.String())
	return err
}

// Even legacy descriptions can contain terminal escape codes. Keep multiline
// declarations readable without allowing them to issue terminal instructions.
func catalogText(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}
