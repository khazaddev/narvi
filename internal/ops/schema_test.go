package ops

import "testing"

func TestDashboard_Validate(t *testing.T) {
	validPanel := Panel{ID: "p1", Title: "Panel 1", Description: "desc", Type: PanelTypeGauge, Metrics: []string{"m1"}, Unit: "s"}

	tests := []struct {
		name    string
		d       Dashboard
		wantErr bool
	}{
		{"valid", Dashboard{Title: "T", Panels: []Panel{validPanel}}, false},
		{"missing title", Dashboard{Panels: []Panel{validPanel}}, true},
		{"no panels", Dashboard{Title: "T"}, true},
		{"panel missing id", Dashboard{Title: "T", Panels: []Panel{{Title: "x", Description: "d", Type: PanelTypeGauge, Metrics: []string{"m"}, Unit: "s"}}}, true},
		{"duplicate panel id", Dashboard{Title: "T", Panels: []Panel{validPanel, validPanel}}, true},
		{"panel missing metrics", Dashboard{Title: "T", Panels: []Panel{{ID: "p1", Title: "x", Description: "d", Type: PanelTypeGauge, Unit: "s"}}}, true},
		{"unknown panel type", Dashboard{Title: "T", Panels: []Panel{{ID: "p1", Title: "x", Description: "d", Type: "bogus", Metrics: []string{"m"}, Unit: "s"}}}, true},
		{"histogram_quantile missing quantile", Dashboard{Title: "T", Panels: []Panel{{ID: "p1", Title: "x", Description: "d", Type: PanelTypeHistogramQuantile, Metrics: []string{"m"}, Unit: "s"}}}, true},
		{"histogram_quantile quantile out of range", Dashboard{Title: "T", Panels: []Panel{{ID: "p1", Title: "x", Description: "d", Type: PanelTypeHistogramQuantile, Metrics: []string{"m"}, Quantile: 1.5, Unit: "s"}}}, true},
		{"histogram_quantile valid", Dashboard{Title: "T", Panels: []Panel{{ID: "p1", Title: "x", Description: "d", Type: PanelTypeHistogramQuantile, Metrics: []string{"m"}, Quantile: 0.95, Unit: "s"}}}, false},
		{"ratio wrong metric count", Dashboard{Title: "T", Panels: []Panel{{ID: "p1", Title: "x", Description: "d", Type: PanelTypeRatio, Metrics: []string{"m"}, Unit: "ratio"}}}, true},
		{"ratio valid", Dashboard{Title: "T", Panels: []Panel{{ID: "p1", Title: "x", Description: "d", Type: PanelTypeRatio, Metrics: []string{"m1", "m2"}, Unit: "ratio"}}}, false},
		{"panel missing unit", Dashboard{Title: "T", Panels: []Panel{{ID: "p1", Title: "x", Description: "d", Type: PanelTypeGauge, Metrics: []string{"m"}}}}, true},
		{"panel missing description", Dashboard{Title: "T", Panels: []Panel{{ID: "p1", Title: "x", Type: PanelTypeGauge, Metrics: []string{"m"}, Unit: "s"}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.d.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAlert_Validate(t *testing.T) {
	valid := Alert{Name: "A", Description: "d", Metrics: []string{"m"}, Condition: "c", Severity: "warning", ThresholdDerivation: "because"}

	tests := []struct {
		name    string
		mutate  func(a Alert) Alert
		wantErr bool
	}{
		{"valid", func(a Alert) Alert { return a }, false},
		{"missing name", func(a Alert) Alert { a.Name = ""; return a }, true},
		{"missing description", func(a Alert) Alert { a.Description = ""; return a }, true},
		{"missing metrics", func(a Alert) Alert { a.Metrics = nil; return a }, true},
		{"missing condition", func(a Alert) Alert { a.Condition = ""; return a }, true},
		{"invalid severity", func(a Alert) Alert { a.Severity = "yolo"; return a }, true},
		{"critical severity ok", func(a Alert) Alert { a.Severity = "critical"; return a }, false},
		{"missing threshold derivation", func(a Alert) Alert { a.ThresholdDerivation = ""; return a }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.mutate(valid).Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckDrift(t *testing.T) {
	registered := map[string]RegisteredInstrument{
		"real_counter_total":     {Name: "real_counter_total", File: "x.go", Line: 1},
		"real_histogram_seconds": {Name: "real_histogram_seconds", File: "x.go", Line: 2},
	}

	t.Run("no drift", func(t *testing.T) {
		dashboards := []Dashboard{{Title: "T", Panels: []Panel{
			{ID: "p1", Title: "x", Description: "d", Type: PanelTypeCounterRate, Metrics: []string{"real_counter_total"}, Unit: "x"},
		}}}
		alerts := []Alert{{Name: "A", Metrics: []string{"real_histogram_seconds"}}}
		if errs := CheckDrift(dashboards, alerts, registered); len(errs) != 0 {
			t.Errorf("CheckDrift() = %v, want none", errs)
		}
	})

	t.Run("dashboard panel references unregistered metric", func(t *testing.T) {
		dashboards := []Dashboard{{Title: "T", Panels: []Panel{
			{ID: "bad-panel", Title: "x", Description: "d", Type: PanelTypeGauge, Metrics: []string{"nonexistent_metric"}, Unit: "x"},
		}}}
		errs := CheckDrift(dashboards, nil, registered)
		if len(errs) != 1 {
			t.Fatalf("CheckDrift() = %v, want exactly 1 error", errs)
		}
		if errs[0].Kind != "dashboard" || errs[0].Item != "bad-panel" || errs[0].Metric != "nonexistent_metric" {
			t.Errorf("CheckDrift() = %+v, want Kind=dashboard Item=bad-panel Metric=nonexistent_metric", errs[0])
		}
	})

	t.Run("alert references unregistered metric", func(t *testing.T) {
		alerts := []Alert{{Name: "BadAlert", Metrics: []string{"nonexistent_metric"}}}
		errs := CheckDrift(nil, alerts, registered)
		if len(errs) != 1 {
			t.Fatalf("CheckDrift() = %v, want exactly 1 error", errs)
		}
		if errs[0].Kind != "alert" || errs[0].Item != "BadAlert" || errs[0].Metric != "nonexistent_metric" {
			t.Errorf("CheckDrift() = %+v, want Kind=alert Item=BadAlert Metric=nonexistent_metric", errs[0])
		}
	})

	t.Run("renamed instrument is caught the same as a never-registered one", func(t *testing.T) {
		// Simulates "rename a registered instrument without updating the
		// dashboard": registered no longer has the old name at all.
		renamed := map[string]RegisteredInstrument{
			"real_counter_total_v2": {Name: "real_counter_total_v2", File: "x.go", Line: 1},
		}
		dashboards := []Dashboard{{Title: "T", Panels: []Panel{
			{ID: "p1", Title: "x", Description: "d", Type: PanelTypeCounterRate, Metrics: []string{"real_counter_total"}, Unit: "x"},
		}}}
		errs := CheckDrift(dashboards, nil, renamed)
		if len(errs) != 1 {
			t.Fatalf("CheckDrift() = %v, want exactly 1 error", errs)
		}
	})
}
