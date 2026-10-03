//nolint:lll // a schema is prose, and a description is one string
package schema

// telemetryValues are the chart's values for its two other things to render,
// beside the write path: alert rules and Grafana dashboards. They are values,
// not configuration of a binary, so they have no schema of their own in
// schemas/config; only the chart's values schema carries them.
func telemetryValues() m {
	// An empty string is a value here: it means the release's own.
	str := func(description string) m { return m{"type": "string", "description": description} }
	rule := func(description string, extra m) m {
		props := m{
			"enabled":  boolean("Render the rule."),
			"severity": str("The severity label."),
			"for":      str("How long the condition holds before the alert fires, as a duration."),
			"labels":   m{"type": "object", "additionalProperties": m{"type": "string"}, "description": "Labels added to this rule only."},
		}
		for k, v := range extra {
			props[k] = v
		}
		return obj(description, props)
	}
	return m{
		"renders": m{"enum": []string{"app", "alerts", "dashboards"}, "description": "What this release renders: `app`, the write path (the default); `alerts`, only the alert rules; `dashboards`, only the Grafana dashboard ConfigMaps. In the last two nothing else is validated or rendered."},
		"alerts": obj("Alert rules, rendered with `renders: alerts`.", m{
			"name":             str("The object's name. Empty: <release>-alerts."),
			"objectNamespace":  str("Where the rule object goes. Empty: the release's namespace."),
			"format":           m{"enum": []string{"vmrule", "prometheusrule"}, "description": "The kind of object."},
			"labels":           m{"type": "object", "additionalProperties": m{"type": "string"}, "description": "Labels on the rule object."},
			"ruleLabels":       m{"type": "object", "additionalProperties": m{"type": "string"}, "description": "Labels added to every rule, for Alertmanager routing, such as `k8s_cluster_name`."},
			"interval":         str("How often the ruler evaluates the group."),
			"clusterLabel":     str("The label that names the cluster on a store that holds several."),
			"namespace":        str("The namespace the write path runs in, whose series the writer rules read. Empty: the release's."),
			"selector":         str("Extra matchers for the writer's series."),
			"emitterNamespace": str("A regex of the namespaces the emitters run in."),
			"emitterSelector":  str("Extra matchers for the emitters' series."),
			"runbookBaseUrl":   str("Where the runbook is; the alert's name, lower-cased, is the anchor. Empty renders no link."),
			"rules": obj("The rules.", m{
				"deadLettered":     rule("Records the writer dead-lettered.", nil),
				"emitterDrops":     rule("Records an emitter's queue gave up.", nil),
				"digestStale":      rule("The newest digest is older than `maxAgeSeconds`.", m{"maxAgeSeconds": integer("The age, in seconds.", 1, nil)}),
				"indexLag":         rule("The index is behind the archive.", m{"thresholdSeconds": integer("The p99 lag, in seconds.", 1, nil)}),
				"indexDeferred":    rule("Rows reached the archive and not the index.", nil),
				"writerRejections": rule("The writer refuses a share of the records it is sent.", m{"ratio": m{"type": "number", "minimum": 0, "maximum": 1, "description": "The share refused."}, "minRecords": integer("The fewest refusals in the window that count.", 1, nil)}),
				"consumerFailing":  rule("A queue consumer's target keeps failing its batches.", nil),
			}),
		}),
		"dashboards": obj("Grafana dashboards, rendered with `renders: dashboards`.", m{
			"namespace":         str("Grafana's namespace. Empty: the release's."),
			"folder":            str("The Grafana folder, for a sidecar that reads the folder annotation."),
			"sidecarLabel":      str("The label Grafana's sidecar selects ConfigMaps by."),
			"sidecarLabelValue": str("Its value."),
			"labels":            m{"type": "object", "additionalProperties": m{"type": "string"}, "description": "Extra labels on each ConfigMap."},
		}),
	}
}

// appOnly makes the checks that hold the write path's configuration apply only
// when the release renders the write path: in `alerts` and `dashboards` mode
// there is no writer config to hold.
func appOnly(all []any) m {
	notApp := m{"properties": m{"renders": m{"enum": []string{"alerts", "dashboards"}}}, "required": []string{"renders"}}
	return m{"if": m{"not": notApp}, "then": m{"allOf": all}}
}
