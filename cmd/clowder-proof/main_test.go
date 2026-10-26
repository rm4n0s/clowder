//go:build clowder_browser

package main

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"github.com/rm4n0s/errors"
)

func TestParseFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    options
		wantTag string
	}{
		{
			name: "defaults",
			args: []string{"-o", "x.png"},
			want: options{page: "/", out: "x.png", width: 1280, height: 800, wait: 500 * time.Millisecond},
		},
		{
			name: "all flags",
			args: []string{"-o", "x.png", "-page", "/rooms/1", "-as", "ana", "-width", "400", "-height", "300", "-wait", "1s"},
			want: options{page: "/rooms/1", out: "x.png", as: "ana", width: 400, height: 300, wait: time.Second},
		},
		{
			name: "sign-up flags",
			args: []string{
				"-o", "x.png", "-invite", "-page", "/invite/{invite}",
				"-fill", "#username=ab", "-fill", "input[name=password]=short=1", "-click", "button[type=submit]",
			},
			want: options{
				page: "/invite/{invite}", out: "x.png", width: 1280, height: 800, wait: 500 * time.Millisecond,
				invite: true, click: "button[type=submit]",
				fills: fills{{"#username", "ab"}, {"input[name=password]", "short=1"}},
			},
		},
		{
			name: "room placeholder",
			args: []string{"-o", "x.png", "-as", "alice", "-page", "{room:secret}"},
			want: options{page: "{room:secret}", out: "x.png", as: "alice", width: 1280, height: 800, wait: 500 * time.Millisecond},
		},
		{name: "room placeholder without a user", args: []string{"-o", "x.png", "-page", "{room:secret}"}, wantTag: tagFlagsInvalid},
		{name: "invite without the placeholder", args: []string{"-o", "x.png", "-invite", "-page", "/invite/x"}, wantTag: tagFlagsInvalid},
		{name: "placeholder without invite", args: []string{"-o", "x.png", "-page", "/invite/{invite}"}, wantTag: tagFlagsInvalid},
		{name: "fill without a value", args: []string{"-o", "x.png", "-fill", "#username"}, wantTag: tagFlagsInvalid},
		{name: "fill without a selector", args: []string{"-o", "x.png", "-fill", "=value"}, wantTag: tagFlagsInvalid},
		{name: "output missing", args: nil, wantTag: tagFlagsInvalid},
		{name: "page without slash", args: []string{"-o", "x.png", "-page", "rooms"}, wantTag: tagFlagsInvalid},
		{name: "zero width", args: []string{"-o", "x.png", "-width", "0"}, wantTag: tagFlagsInvalid},
		{name: "negative wait", args: []string{"-o", "x.png", "-wait", "-1s"}, wantTag: tagFlagsInvalid},
		{name: "unknown flag", args: []string{"-nope"}, wantTag: tagFlagsInvalid},
		{name: "extra argument", args: []string{"-o", "x.png", "more"}, wantTag: tagFlagsInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			got, err := parseFlags(tt.args, &stderr)
			if tt.wantTag == "" {
				if err != nil {
					t.Fatalf("parseFlags: %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("options = %+v, want %+v", got, tt.want)
				}
				return
			}
			appErr, ok := errors.FromError(err)
			if !ok {
				t.Fatalf("expected *errors.Error, got %T: %v", err, err)
			}
			if appErr.Tag != tt.wantTag || !appErr.HasRoute("parseFlags.FlagsInvalid") {
				t.Errorf("tag = %q, route = %s", appErr.Tag, appErr.Route())
			}
		})
	}
}

func TestSplitFill(t *testing.T) {
	tests := []struct {
		in           string
		wantSelector string
		wantValue    string
		wantOK       bool
	}{
		{"#username=ab", "#username", "ab", true},
		{"#username=", "#username", "", true},
		{"input[name=username]=ab", "input[name=username]", "ab", true},
		{`input[name="a=b"]=x`, `input[name="a=b"]`, "x", true},
		{`input[name='a]=b']=x`, `input[name='a]=b']`, "x", true},
		{"#p=a=b=c", "#p", "a=b=c", true},
		{"#p=with spaces", "#p", "with spaces", true},
		{"#p", "", "", false},
		{"=x", "", "", false},
		{"", "", "", false},
		{"input[name=x]", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			selector, value, ok := splitFill(tt.in)
			if selector != tt.wantSelector || value != tt.wantValue || ok != tt.wantOK {
				t.Errorf("splitFill(%q) = %q, %q, %v; want %q, %q, %v",
					tt.in, selector, value, ok, tt.wantSelector, tt.wantValue, tt.wantOK)
			}
		})
	}
}

func TestFillsString(t *testing.T) {
	f := fills{{"#a", "1"}, {"#b", "2"}}
	if got := f.String(); got != "#a=1 #b=2" {
		t.Errorf("String = %q", got)
	}
}
