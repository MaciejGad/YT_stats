package web

import (
	"log/slog"
	"net/http"
)

// NewHandler zwraca główny handler HTTP.
//
// Krok 1: szkielet bez tras. Trasy, sesje i nagłówki bezpieczeństwa
// dochodzą w krokach 3 i 4 planu.
func NewHandler(files *Files, log *slog.Logger) http.Handler {
	_ = files
	_ = log
	return http.NotFoundHandler()
}
