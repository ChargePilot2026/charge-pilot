package admin

import (
	"bytes"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestExportFormats(t *testing.T) {
	headers := []string{"账单号", "电费(分)", "服务费(分)", "合计(分)", "退款(分)"}
	lines := [][]string{{"BILL-001", "200", "50", "250", "0"}, {"=1+1", "300", "75", "375", "25"}}
	period := map[string]any{"from": "2026-09-01", "to": "2026-09-30"}
	for _, format := range []string{"csv", "xlsx", "pdf"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := writeExport(&output, format, "bills", headers, lines, period); err != nil {
				t.Fatal(err)
			}
			switch format {
			case "csv":
				if !strings.Contains(output.String(), "'=1+1") {
					t.Fatalf("CSV formula was not escaped: %s", output.String())
				}
			case "xlsx":
				book, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
				if err != nil {
					t.Fatal(err)
				}
				defer book.Close()
				value, err := book.GetCellValue("Sheet1", "A3")
				if err != nil || value != "'=1+1" {
					t.Fatalf("XLSX formula was not escaped: %q %v", value, err)
				}
			case "pdf":
				if !bytes.HasPrefix(output.Bytes(), []byte("%PDF-")) {
					t.Fatal("PDF header missing")
				}
			}
		})
	}
}

func TestExportPeriodAndPDFScope(t *testing.T) {
	for _, tc := range []struct {
		filter map[string]any
		ok     bool
	}{
		{map[string]any{"from": "2026-09-01", "to": "2026-09-30"}, true},
		{map[string]any{"from": "2026-09-30", "to": "2026-09-01"}, false},
		{map[string]any{"from": "2026-09-01", "to": "2026-10-02"}, false},
		{map[string]any{"from": "bad", "to": "2026-09-30"}, false},
	} {
		if validExportPeriod(tc.filter) != tc.ok {
			t.Fatalf("period validation mismatch: %v", tc.filter)
		}
	}
	var output bytes.Buffer
	if err := writeExport(&output, "pdf", "orders", nil, nil, map[string]any{"from": "2026-09-01", "to": "2026-09-30"}); err == nil {
		t.Fatal("PDF must only support summary resources")
	}
	output.Reset()
	if err := writeExport(&output, "pdf", "reconciles",
		[]string{"内部笔数", "渠道笔数", "差异笔数", "内部金额(分)", "渠道金额(分)", "差额(分)"},
		[][]string{{"2", "1", "1", "250", "200", "50"}},
		map[string]any{"from": "2026-09-01", "to": "2026-09-30"}); err != nil || !bytes.HasPrefix(output.Bytes(), []byte("%PDF-")) {
		t.Fatalf("reconciliation PDF failed: %v", err)
	}
}
