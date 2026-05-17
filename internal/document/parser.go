package document

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Extractor handles text extraction from PDF and TXT files.
type Extractor struct {
	tmpDir string
}

// NewExtractor creates a new text extractor.
func NewExtractor(tmpDir string) *Extractor {
	return &Extractor{tmpDir: tmpDir}
}

// ExtractText extracts text from a file (PDF via pdftotext, TXT directly).
func (e *Extractor) ExtractText(filePath string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filePath))

	switch ext {
	case ".txt", ".md":
		return e.extractPlain(filePath)
	case ".pdf":
		return e.extractPDF(filePath)
	default:
		return "", fmt.Errorf("tipo de archivo no soportado: %s", ext)
	}
}

func (e *Extractor) extractPlain(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("leyendo archivo de texto: %w", err)
	}
	return string(data), nil
}

func (e *Extractor) extractPDF(filePath string) (string, error) {
	// Check pdftotext is available
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return "", fmt.Errorf("pdftotext no encontrado, instala poppler-utils: %w", err)
	}

	cmd := exec.Command("pdftotext", "-layout", filePath, "-")
	output, err := cmd.Output()
	if err != nil {
		// Try to capture stderr for more details
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("extrayendo PDF: %s", string(exitErr.Stderr))
		}
		return "", fmt.Errorf("extrayendo PDF: %w", err)
	}

	text := strings.TrimSpace(string(output))
	if text == "" {
		return "", fmt.Errorf("PDF sin texto extraíble (posiblemente escaneado)")
	}

	return text, nil
}
