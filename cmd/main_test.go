/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"context"
	"log/slog"
	"testing"
)

func TestSetupLogger_DefaultLevel(t *testing.T) {
	logger := setupLogger("info", "json")
	if logger == nil {
		t.Fatal("logger is nil")
	}
	if _, ok := logger.Handler().(*slog.JSONHandler); !ok {
		t.Errorf("expected JSONHandler for format=json, got %T", logger.Handler())
	}
}

func TestSetupLogger_DebugLevel(t *testing.T) {
	logger := setupLogger("debug", "json")
	if logger == nil {
		t.Fatal("logger is nil")
	}
	if !logger.Handler().Enabled(context.Background(), slog.LevelDebug) {
		t.Error("expected debug level enabled")
	}
}

func TestSetupLogger_WarnLevel(t *testing.T) {
	logger := setupLogger("warn", "json")
	if logger == nil {
		t.Fatal("logger is nil")
	}
	ctx := context.Background()
	if logger.Handler().Enabled(ctx, slog.LevelInfo) {
		t.Error("expected info level disabled")
	}
	if !logger.Handler().Enabled(ctx, slog.LevelWarn) {
		t.Error("expected warn level enabled")
	}
}

func TestSetupLogger_ErrorLevel(t *testing.T) {
	logger := setupLogger("error", "json")
	if logger == nil {
		t.Fatal("logger is nil")
	}
	ctx := context.Background()
	if logger.Handler().Enabled(ctx, slog.LevelWarn) {
		t.Error("expected warn level disabled")
	}
	if !logger.Handler().Enabled(ctx, slog.LevelError) {
		t.Error("expected error level enabled")
	}
}

func TestSetupLogger_UnknownLevelDefaultsToInfo(t *testing.T) {
	logger := setupLogger("garbage", "json")
	if logger == nil {
		t.Fatal("logger is nil")
	}
	ctx := context.Background()
	if !logger.Handler().Enabled(ctx, slog.LevelInfo) {
		t.Error("expected info level enabled")
	}
	if logger.Handler().Enabled(ctx, slog.LevelDebug) {
		t.Error("expected debug level disabled")
	}
}

func TestSetupLogger_TextFormat(t *testing.T) {
	logger := setupLogger("info", "text")
	if logger == nil {
		t.Fatal("logger is nil")
	}
	if _, ok := logger.Handler().(*slog.TextHandler); !ok {
		t.Errorf("expected TextHandler for format=text, got %T", logger.Handler())
	}
}

func TestSetupLogger_UnknownFormatDefaultsToJSON(t *testing.T) {
	logger := setupLogger("info", "garbage")
	if logger == nil {
		t.Fatal("logger is nil")
	}
	if _, ok := logger.Handler().(*slog.JSONHandler); !ok {
		t.Errorf("expected JSONHandler for unknown format, got %T", logger.Handler())
	}
}

func TestSetupLogger_CaseInsensitiveLevel(t *testing.T) {
	logger := setupLogger("DEBUG", "json")
	if logger == nil {
		t.Fatal("logger is nil")
	}
	if !logger.Handler().Enabled(context.Background(), slog.LevelDebug) {
		t.Error("expected debug level enabled (uppercase input)")
	}
}

func TestSetupLogger_CaseInsensitiveFormat(t *testing.T) {
	logger := setupLogger("info", "TEXT")
	if logger == nil {
		t.Fatal("logger is nil")
	}
	if _, ok := logger.Handler().(*slog.TextHandler); !ok {
		t.Errorf("expected TextHandler for format=TEXT (uppercase), got %T", logger.Handler())
	}
}
