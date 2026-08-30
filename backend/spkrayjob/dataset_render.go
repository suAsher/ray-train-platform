package spkrayjob

import (
	"fmt"
	"io"
	"text/tabwriter"
)

func renderDatasets(writer io.Writer, items []DatasetCatalogItem) error {
	if len(items) == 0 {
		_, err := fmt.Fprintln(writer, "没有可用数据集。请联系团队管理员发布数据集版本。")
		return err
	}
	table := tabwriter.NewWriter(writer, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "SLUG\tNAME\tVISIBILITY\tSCHEMA\tSOURCE")
	for _, item := range items {
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s/%s\n", item.Slug, item.Name, item.Visibility, item.SchemaVersion, item.SourceSpace, item.SourceRelativePath)
	}
	return table.Flush()
}

func renderDatasetVersions(writer io.Writer, dataset DatasetCatalogItem, versions []DatasetVersionCatalogItem) error {
	if len(versions) == 0 {
		_, err := fmt.Fprintf(writer, "数据集 %s 还没有版本。\n", dataset.Slug)
		return err
	}
	if _, err := fmt.Fprintf(writer, "数据集：%s（%s）\n", dataset.Name, dataset.Slug); err != nil {
		return err
	}
	table := tabwriter.NewWriter(writer, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "VERSION ID\tVERSION\tSTATE\tTRAIN\tVAL\tPACKED\tSCHEMA")
	for _, version := range versions {
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%d\t%s\t%s\n",
			version.ID, version.Version, version.State, version.TrainSamples, version.ValSamples, formatIECBytes(version.PackedBytes), version.SchemaVersion)
	}
	return table.Flush()
}

func renderStreamingPreflight(writer io.Writer, result SubmissionPreflightResult) error {
	if result.Dataset == nil {
		return nil
	}
	dataset := result.Dataset
	_, err := fmt.Fprintf(writer,
		"预检通过：数据集 %s，固定版本 %s（训练 %d / 验证 %d，打包 %s），镜像 %s，%d GPU，缓存 %s。\n",
		dataset.DatasetSlug, dataset.VersionID, dataset.TrainSamples, dataset.ValSamples, formatIECBytes(dataset.PackedBytes),
		result.Image, result.RequestedGPUs, dataset.CachePolicy)
	return err
}

func formatIECBytes(value int64) string {
	if value < 0 {
		return "-"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	amount := float64(value)
	unit := 0
	for amount >= 1024 && unit < len(units)-1 {
		amount /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", value)
	}
	return fmt.Sprintf("%.1f %s", amount, units[unit])
}
