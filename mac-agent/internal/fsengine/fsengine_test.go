package fsengine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestSandboxAndOptimisticLockAndExcel(t *testing.T) {
	root := t.TempDir()
	eng := New([]string{root})

	// Path traversal must be rejected
	if _, err := eng.ResolvePath(filepath.Join(root, "..", "etc", "passwd"), false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden on traversal, got %v", err)
	}
	// Hard blocked secret key file
	pemPath := filepath.Join(root, "prod.pem")
	_ = os.WriteFile(pemPath, []byte("PRIVATE KEY"), 0o600)
	if _, err := eng.ResolvePath(pemPath, false); !errors.Is(err, ErrForbidden) {
		t.Fatalf("expected ErrForbidden on .pem file, got %v", err)
	}

	// Text read + write + optimistic lock conflict
	hello := filepath.Join(root, "hello.go")
	_ = os.WriteFile(hello, []byte("package main\n"), 0o644)
	r1, err := eng.ReadText(hello, 1, 200, false)
	if err != nil || !r1.Editable || r1.SHA256 == "" {
		t.Fatalf("ReadText failed: %+v err=%v", r1, err)
	}
	w1, err := eng.WriteText(hello, "package main\n// edited\n", r1.SHA256)
	if err != nil || w1.SHA256 == r1.SHA256 {
		t.Fatalf("WriteText failed: %+v err=%v", w1, err)
	}
	// Stale sha256 must return ConflictError
	if _, err := eng.WriteText(hello, "stale overwrite", r1.SHA256); err == nil {
		t.Fatalf("expected conflict error on stale sha256")
	} else {
		var ce *ConflictError
		if !errors.As(err, &ce) || ce.CurrentSHA256 != w1.SHA256 {
			t.Fatalf("expected ConflictError with current sha, got %v", err)
		}
	}

	// Excel read + cell edit roundtrip
	xlsxPath := filepath.Join(root, "demo.xlsx")
	xf := excelize.NewFile()
	_ = xf.SetCellValue("Sheet1", "A1", "Name")
	_ = xf.SetCellValue("Sheet1", "B1", "Score")
	_ = xf.SetCellValue("Sheet1", "A2", "Alice")
	_ = xf.SetCellValue("Sheet1", "B2", "95")
	if err := xf.SaveAs(xlsxPath); err != nil {
		t.Fatalf("save xlsx: %v", err)
	}
	_ = xf.Close()

	sh, err := eng.ExcelSheet(xlsxPath, "Sheet1", 1, 50)
	if err != nil || len(sh.Rows) != 2 || sh.Rows[1][0] != "Alice" {
		t.Fatalf("ExcelSheet failed: %+v err=%v", sh, err)
	}
	if _, err := eng.ExcelWriteCell(xlsxPath, "Sheet1", 2, 2, "100", sh.SHA256); err != nil {
		t.Fatalf("ExcelWriteCell failed: %v", err)
	}
	sh2, err := eng.ExcelSheet(xlsxPath, "Sheet1", 1, 50)
	if err != nil || sh2.Rows[1][1] != "100" {
		t.Fatalf("expected updated cell=100, got %+v err=%v", sh2, err)
	}
}
