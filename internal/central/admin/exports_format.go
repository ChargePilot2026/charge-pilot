package admin

import (
	_ "embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"codeberg.org/go-pdf/fpdf"
	"github.com/xuri/excelize/v2"
)

// reportFont 是编译期内嵌的中文 PDF 字体子集，汇总 PDF 的标题与数字都用它，
// 避免 PDF 阅读器因为找不到中文字体而显示成方块。
//
//go:embed fonts/NotoSansSC-Subset.ttf
var reportFont []byte

// validExportPeriod 校验财务类导出的时间窗：只认 from/to 两个 YYYY-MM-DD 键，
// 要求 to 不早于 from 且跨度不超过 31 天，防止一次导出拖垮数据库。
func validExportPeriod(filter map[string]any) bool {
	fromRaw, fromOK := filter["from"].(string)
	toRaw, toOK := filter["to"].(string)
	if !fromOK || !toOK || len(filter) != 2 {
		return false
	}
	from, err1 := time.Parse("2006-01-02", fromRaw)
	to, err2 := time.Parse("2006-01-02", toRaw)
	return err1 == nil && err2 == nil && !to.Before(from) && to.Sub(from) <= 30*24*time.Hour
}

// nextExportDate 把 to 当天推一天，用于把 issued_at 的右开区间凑成整天的闭区间。
func nextExportDate(raw any) string {
	date, _ := time.Parse("2006-01-02", raw.(string))
	return date.AddDate(0, 0, 1).Format("2006-01-02")
}

// safeSpreadsheetCell 防止表格公式注入：首字符是 = + - @ 且整串不是数字时，前面补一个单引号；
// 纯数字（负数金额）保持原样，不影响导出的取值。
func safeSpreadsheetCell(value string) string {
	if value == "" {
		return value
	}
	if strings.ContainsRune("=+-@", rune(value[0])) {
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			return "'" + value
		}
	}
	return value
}

// writeExport 按格式把表头和数据行写出去：csv 走标准转义，xlsx 走流式写入避免大文件占内存，
// pdf 只支持账单与对账的汇总页，其余格式或资源一律返回错误。
func writeExport(w io.Writer, format, resource string, headers []string, lines [][]string, period map[string]any) error {
	switch format {
	case "csv":
		writer := csv.NewWriter(w)
		if err := writer.Write(headers); err != nil {
			return err
		}
		for _, line := range lines {
			protected := make([]string, len(line))
			for i, value := range line {
				protected[i] = safeSpreadsheetCell(value)
			}
			if err := writer.Write(protected); err != nil {
				return err
			}
		}
		writer.Flush()
		return writer.Error()
	case "xlsx":
		book := excelize.NewFile()
		defer book.Close()
		stream, err := book.NewStreamWriter("Sheet1")
		if err != nil {
			return err
		}
		for row, line := range append([][]string{headers}, lines...) {
			cells := make([]interface{}, len(line))
			for i, value := range line {
				cells[i] = safeSpreadsheetCell(value)
			}
			axis, err := excelize.CoordinatesToCellName(1, row+1)
			if err != nil {
				return err
			}
			if err := stream.SetRow(axis, cells); err != nil {
				return err
			}
		}
		if err := stream.Flush(); err != nil {
			return err
		}
		return book.Write(w)
	case "pdf":
		return writeSummaryPDF(w, resource, headers, lines, period)
	default:
		return errors.New("unsupported export format")
	}
}

// writeSummaryPDF 生成财务汇总 PDF：按中文表头定位需要合计的列并求和，
// 输出标题、统计期间、记录数、各列合计与生成时间；列缺失或值不是整数都会直接报错。
func writeSummaryPDF(w io.Writer, resource string, headers []string, lines [][]string, period map[string]any) error {
	var title string
	var columns []string
	switch resource {
	case "bills":
		title = "充电账单汇总"
		columns = []string{"电费(分)", "服务费(分)", "合计(分)", "退款(分)"}
	case "reconciles":
		title = "对账汇总"
		columns = []string{"内部笔数", "渠道笔数", "差异笔数", "内部金额(分)", "渠道金额(分)", "差额(分)"}
	default:
		return errors.New("PDF summary supports bills and reconciles only")
	}
	index := map[string]int{}
	for i, header := range headers {
		index[header] = i
	}
	totals := map[string]int64{}
	for _, column := range columns {
		i, ok := index[column]
		if !ok {
			return fmt.Errorf("PDF summary missing %s", column)
		}
		for _, line := range lines {
			if i >= len(line) {
				return errors.New("PDF summary row is incomplete")
			}
			value, err := strconv.ParseInt(line[i], 10, 64)
			if err != nil {
				return fmt.Errorf("PDF summary value for %s is unavailable: %w", column, err)
			}
			totals[column] += value
		}
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddUTF8FontFromBytes("NotoSC", "", reportFont)
	pdf.AddPage()
	pdf.SetFont("NotoSC", "", 18)
	pdf.Cell(0, 14, title)
	pdf.Ln(18)
	pdf.SetFont("NotoSC", "", 11)
	pdf.Cell(0, 9, "统计期间: "+period["from"].(string)+" - "+period["to"].(string))
	pdf.Ln(11)
	pdf.Cell(0, 9, "记录数: "+strconv.Itoa(len(lines)))
	pdf.Ln(11)
	for _, column := range columns {
		pdf.Cell(0, 9, column+": "+strconv.FormatInt(totals[column], 10))
		pdf.Ln(10)
	}
	pdf.Cell(0, 9, "生成时间: "+time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	return pdf.Output(w)
}
