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

//go:embed fonts/NotoSansSC-Subset.ttf
var reportFont []byte

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

func nextExportDate(raw any) string {
	date, _ := time.Parse("2006-01-02", raw.(string))
	return date.AddDate(0, 0, 1).Format("2006-01-02")
}

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
