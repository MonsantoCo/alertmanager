// Copyright 2015 Prometheus Team
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"errors"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/prometheus/common/model"
	"gopkg.in/yaml.v2"
)

var patAuthLine = regexp.MustCompile(`((?:api_token|api_key|service_key|api_url|auth_token):\s+)(".+"|'.+'|[^\s]+)`)

// Secret is a string that must not be revealed on marshaling.
type Secret string

// MarshalYAML implements the yaml.Marshaler interface.
func (s Secret) MarshalYAML() (interface{}, error) {
	return "<hidden>", nil
}

// Load parses the YAML input s into a Config.
func Load(s string) (*Config, error) {
	cfg := &Config{}
	err := yaml.Unmarshal([]byte(s), cfg)
	if err != nil {
		return nil, err
	}
	// Check if we have a root route. We cannot check for it in the
	// UnmarshalYAML method because it won't be called if the input is empty
	// (e.g. the config file is empty or only contains whitespace).
	if cfg.Route == nil {
		return nil, errors.New("no route provided in config")
	}

	cfg.original = s
	return cfg, nil
}

// LoadFile parses the given YAML file into a Config.
func LoadFile(filename string) (*Config, error) {
	content, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	cfg, err := Load(string(content))
	if err != nil {
		return nil, err
	}

	resolveFilepaths(filepath.Dir(filename), cfg)
	return cfg, nil
}

// resolveFilepaths joins all relative paths in a configuration
// with a given base directory.
func resolveFilepaths(baseDir string, cfg *Config) {
	join := func(fp string) string {
		if len(fp) > 0 && !filepath.IsAbs(fp) {
			fp = filepath.Join(baseDir, fp)
		}
		return fp
	}

	for i, tf := range cfg.Templates {
		cfg.Templates[i] = join(tf)
	}
}

// Config is the top-level configuration for Alertmanager's config files.
type Config struct {
	Global       *GlobalConfig  `yaml:"global,omitempty"`
	Route        *Route         `yaml:"route,omitempty"`
	InhibitRules []*InhibitRule `yaml:"inhibit_rules,omitempty"`
	Receivers    []*Receiver    `yaml:"receivers,omitempty"`
	Templates    []string       `yaml:"templates"`

	// Catches all undefined fields and must be empty after parsing.
	XXX map[string]interface{} `yaml:",inline"`

	// original is the input from which the config was parsed.
	original string
}

func checkOverflow(m map[string]interface{}, ctx string) error {
	if len(m) > 0 {
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		return fmt.Errorf("unknown fields in %s: %s", ctx, strings.Join(keys, ", "))
	}
	return nil
}

func (c Config) String() string {
	var s string
	if c.original != "" {
		s = c.original
	} else {
		b, err := yaml.Marshal(c)
		if err != nil {
			return fmt.Sprintf("<error creating config string: %s>", err)
		}
		s = string(b)
	}
	return patAuthLine.ReplaceAllString(s, "${1}<hidden>")
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (c *Config) UnmarshalYAML(unmarshal func(interface{}) error) error {
	// We want to set c to the defaults and then overwrite it with the input.
	// To make unmarshal fill the plain data struct rather than calling UnmarshalYAML
	// again, we have to hide it using a type indirection.
	type plain Config
	if err := unmarshal((*plain)(c)); err != nil {
		return err
	}

	// If a global block was open but empty the default global config is overwritten.
	// We have to restore it here.
	if c.Global == nil {
		c.Global = &GlobalConfig{}
		*c.Global = DefaultGlobalConfig
	}

	names := map[string]struct{}{}

	for _, rcv := range c.Receivers {
		if _, ok := names[rcv.Name]; ok {
			return fmt.Errorf("notification config name %q is not unique", rcv.Name)
		}
		for _, ec := range rcv.EmailConfigs {
			if ec.Smarthost == "" {
				if c.Global.SMTPSmarthost == "" {
					return fmt.Errorf("no global SMTP smarthost set")
				}
				ec.Smarthost = c.Global.SMTPSmarthost
			}
			if ec.From == "" {
				if c.Global.SMTPFrom == "" {
					return fmt.Errorf("no global SMTP from set")
				}
				ec.From = c.Global.SMTPFrom
			}
		}
		for _, sc := range rcv.SlackConfigs {
			if sc.APIURL == "" {
				if c.Global.SlackAPIURL == "" {
					return fmt.Errorf("no global Slack API URL set")
				}
				sc.APIURL = c.Global.SlackAPIURL
			}
		}
		for _, hc := range rcv.HipchatConfigs {
			if hc.APIURL == "" {
				if c.Global.HipchatURL == "" {
					return fmt.Errorf("no global Hipchat API URL set")
				}
				hc.APIURL = c.Global.HipchatURL
			}
			if !strings.HasSuffix(hc.APIURL, "/") {
				hc.APIURL += "/"
			}
			if hc.AuthToken == "" {
				if c.Global.HipchatAuthToken == "" {
					return fmt.Errorf("no global Hipchat Auth Token set")
				}
				hc.AuthToken = c.Global.HipchatAuthToken
			}
		}
		for _, pdc := range rcv.PagerdutyConfigs {
			if pdc.URL == "" {
				if c.Global.PagerdutyURL == "" {
					return fmt.Errorf("no global PagerDuty URL set")
				}
				pdc.URL = c.Global.PagerdutyURL
			}
		}
		for _, ogc := range rcv.OpsGenieConfigs {
			if ogc.APIHost == "" {
				if c.Global.OpsGenieAPIHost == "" {
					return fmt.Errorf("no global OpsGenie URL set")
				}
				ogc.APIHost = c.Global.OpsGenieAPIHost
			}
			if !strings.HasSuffix(ogc.APIHost, "/") {
				ogc.APIHost += "/"
			}
		}
		names[rcv.Name] = struct{}{}
	}
	return checkOverflow(c.XXX, "config")
}

// DefaultGlobalConfig provides global default values.
var DefaultGlobalConfig = GlobalConfig{
	ResolveTimeout: model.Duration(5 * time.Minute),

	PagerdutyURL:    "https://events.pagerduty.com/generic/2010-04-15/create_event.json",
	HipchatURL:      "https://api.hipchat.com/",
	OpsGenieAPIHost: "https://api.opsgenie.com/",
}

// GlobalConfig defines configuration parameters that are valid globally
// unless overwritten.
type GlobalConfig struct {
	// ResolveTimeout is the time after which an alert is declared resolved
	// if it has not been updated.
	ResolveTimeout model.Duration `yaml:"resolve_timeout"`

	SMTPFrom         string `yaml:"smtp_from"`
	SMTPSmarthost    string `yaml:"smtp_smarthost"`
	SlackAPIURL      Secret `yaml:"slack_api_url"`
	PagerdutyURL     string `yaml:"pagerduty_url"`
	HipchatURL       string `yaml:"hipchat_url"`
	HipchatAuthToken Secret `yaml:"hipchat_auth_token"`
	OpsGenieAPIHost  string `yaml:"opsgenie_api_host"`
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (c *GlobalConfig) UnmarshalYAML(unmarshal func(interface{}) error) error {
	*c = DefaultGlobalConfig
	type plain GlobalConfig
	return unmarshal((*plain)(c))
}

// A Route is a node that contains definitions of how to handle alerts.
type Route struct {
	Receiver string            `yaml:"receiver,omitempty"`
	GroupBy  []model.LabelName `yaml:"group_by,omitempty"`

	Match    map[string]string `yaml:"match,omitempty"`
	MatchRE  map[string]Regexp `yaml:"match_re,omitempty"`
	Continue bool              `yaml:"continue,omitempty"`
	Routes   []*Route          `yaml:"routes,omitempty"`

	GroupWait      *model.Duration `yaml:"group_wait,omitempty"`
	GroupInterval  *model.Duration `yaml:"group_interval,omitempty"`
	RepeatInterval *model.Duration `yaml:"repeat_interval,omitempty"`

	// Catches all undefined fields and must be empty after parsing.
	XXX map[string]interface{} `yaml:",inline"`
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (r *Route) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type plain Route
	if err := unmarshal((*plain)(r)); err != nil {
		return err
	}

	for k := range r.Match {
		if !model.LabelNameRE.MatchString(k) {
			return fmt.Errorf("invalid label name %q", k)
		}
	}

	for k := range r.MatchRE {
		if !model.LabelNameRE.MatchString(k) {
			return fmt.Errorf("invalid label name %q", k)
		}
	}

	groupBy := map[model.LabelName]struct{}{}

	for _, ln := range r.GroupBy {
		if _, ok := groupBy[ln]; ok {
			return fmt.Errorf("duplicated label %q in group_by", ln)
		}
		groupBy[ln] = struct{}{}
	}

	return checkOverflow(r.XXX, "route")
}

// InhibitRule defines an inhibition rule that mutes alerts that match the
// target labels if an alert matching the source labels exists.
// Both alerts have to have a set of labels being equal.
type InhibitRule struct {
	// SourceMatch defines a set of labels that have to equal the given
	// value for source alerts.
	SourceMatch map[string]string `yaml:"source_match"`
	// SourceMatchRE defines pairs like SourceMatch but does regular expression
	// matching.
	SourceMatchRE map[string]Regexp `yaml:"source_match_re"`
	// TargetMatch defines a set of labels that have to equal the given
	// value for target alerts.
	TargetMatch map[string]string `yaml:"target_match"`
	// TargetMatchRE defines pairs like TargetMatch but does regular expression
	// matching.
	TargetMatchRE map[string]Regexp `yaml:"target_match_re"`
	// A set of labels that must be equal between the source and target alert
	// for them to be a match.
	Equal model.LabelNames `yaml:"equal"`

	// Catches all undefined fields and must be empty after parsing.
	XXX map[string]interface{} `yaml:",inline"`
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (r *InhibitRule) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type plain InhibitRule
	if err := unmarshal((*plain)(r)); err != nil {
		return err
	}

	for k := range r.SourceMatch {
		if !model.LabelNameRE.MatchString(k) {
			return fmt.Errorf("invalid label name %q", k)
		}
	}

	for k := range r.SourceMatchRE {
		if !model.LabelNameRE.MatchString(k) {
			return fmt.Errorf("invalid label name %q", k)
		}
	}

	for k := range r.TargetMatch {
		if !model.LabelNameRE.MatchString(k) {
			return fmt.Errorf("invalid label name %q", k)
		}
	}

	for k := range r.TargetMatchRE {
		if !model.LabelNameRE.MatchString(k) {
			return fmt.Errorf("invalid label name %q", k)
		}
	}

	return checkOverflow(r.XXX, "inhibit rule")
}

// Receiver configuration provides configuration on how to contact a receiver.
type Receiver struct {
	// A unique identifier for this receiver.
	Name string `yaml:"name"`

	EmailConfigs     []*EmailConfig     `yaml:"email_configs,omitempty"`
	PagerdutyConfigs []*PagerdutyConfig `yaml:"pagerduty_configs,omitempty"`
	HipchatConfigs   []*HipchatConfig   `yaml:"hipchat_configs,omitempty"`
	SlackConfigs     []*SlackConfig     `yaml:"slack_configs,omitempty"`
	WebhookConfigs   []*WebhookConfig   `yaml:"webhook_configs,omitempty"`
	OpsGenieConfigs  []*OpsGenieConfig  `yaml:"opsgenie_configs,omitempty"`

	// Catches all undefined fields and must be empty after parsing.
	XXX map[string]interface{} `yaml:",inline"`
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (c *Receiver) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type plain Receiver
	if err := unmarshal((*plain)(c)); err != nil {
		return err
	}
	if c.Name == "" {
		return fmt.Errorf("missing name in receiver")
	}
	return checkOverflow(c.XXX, "receiver config")
}

// Regexp encapsulates a regexp.Regexp and makes it YAML marshalable.
type Regexp struct {
	*regexp.Regexp
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (re *Regexp) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	regex, err := regexp.Compile("^(?:" + s + ")$")
	if err != nil {
		return err
	}
	re.Regexp = regex
	return nil
}

// MarshalYAML implements the yaml.Marshaler interface.
func (re *Regexp) MarshalYAML() (interface{}, error) {
	if re != nil {
		return re.String(), nil
	}
	return nil, nil
}
