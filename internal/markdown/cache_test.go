package markdown

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func fenced(lang, code string) string {
	return "```" + lang + "\n" + code + "```\n"
}

func TestCodeCacheEviction(t *testing.T) {
	k := func(s string) [32]byte { return codeCacheKey("go", s) }
	tests := []struct {
		name      string
		limit     int
		entry     int
		puts      []string // each value is the html of that length (repeated "x")
		gets      []string // gets done after the puts, in order
		wantKept  []string
		wantGone  []string
		wantBytes int
	}{
		{
			name: "everything fits", limit: 100, entry: 100,
			puts: []string{"a:10", "b:10", "c:10"}, wantKept: []string{"a", "b", "c"}, wantBytes: 30,
		},
		{
			name: "the least recently put goes first", limit: 25, entry: 100,
			puts: []string{"a:10", "b:10", "c:10"}, wantKept: []string{"b", "c"}, wantGone: []string{"a"}, wantBytes: 20,
		},
		{
			name: "a get makes an entry recent", limit: 25, entry: 100,
			puts: []string{"a:10", "b:10"}, gets: []string{"a"},
			// c pushes out b, not a
			wantKept: []string{"a"}, wantGone: nil, wantBytes: 20,
		},
		{
			name: "an entry over the entry limit is not kept", limit: 100, entry: 15,
			puts: []string{"a:10", "big:16"}, wantKept: []string{"a"}, wantGone: []string{"big"}, wantBytes: 10,
		},
		{
			name: "an entry over the total is not kept", limit: 5, entry: 100,
			puts: []string{"a:6"}, wantGone: []string{"a"}, wantBytes: 0,
		},
		{
			name: "a put of a key that is there replaces it", limit: 100, entry: 100,
			puts: []string{"a:10", "a:30"}, wantKept: []string{"a"}, wantBytes: 30,
		},
		{
			name: "one big entry pushes several out", limit: 30, entry: 100,
			puts: []string{"a:10", "b:10", "c:10", "d:25"}, wantKept: []string{"d"}, wantGone: []string{"a", "b", "c"}, wantBytes: 25,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCodeCache(tt.limit, tt.entry)
			for _, p := range tt.puts {
				var name string
				var n int
				if _, err := fmt.Sscanf(strings.Replace(p, ":", " ", 1), "%s %d", &name, &n); err != nil {
					t.Fatal(err)
				}
				c.put(k(name), strings.Repeat("x", n))
			}
			for _, g := range tt.gets {
				if _, ok := c.get(k(g)); !ok {
					t.Fatalf("get %s: not kept", g)
				}
			}
			if tt.name == "a get makes an entry recent" {
				c.put(k("c"), strings.Repeat("x", 10))
				if _, ok := c.get(k("b")); ok {
					t.Error("b is kept: the get of a did not make a recent")
				}
			}
			for _, name := range tt.wantKept {
				if _, ok := c.get(k(name)); !ok {
					t.Errorf("%s is not kept", name)
				}
			}
			for _, name := range tt.wantGone {
				if _, ok := c.get(k(name)); ok {
					t.Errorf("%s is kept", name)
				}
			}
			if c.bytes != tt.wantBytes && tt.name != "a get makes an entry recent" {
				t.Errorf("bytes = %d, want %d", c.bytes, tt.wantBytes)
			}
			if c.bytes > tt.limit {
				t.Errorf("bytes = %d over the limit %d", c.bytes, tt.limit)
			}
			if got := c.order.Len(); got != len(c.entries) {
				t.Errorf("list has %d, map has %d", got, len(c.entries))
			}
		})
	}
}

func TestCodeCacheKey(t *testing.T) {
	tests := []struct {
		name       string
		a, b       [2]string
		wantSameAs bool
	}{
		{"same", [2]string{"Go", "x"}, [2]string{"Go", "x"}, true},
		{"another code", [2]string{"Go", "x"}, [2]string{"Go", "y"}, false},
		{"another lexer", [2]string{"Go", "x"}, [2]string{"Python", "x"}, false},
		{"the zero byte separates", [2]string{"a", "bc"}, [2]string{"ab", "c"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codeCacheKey(tt.a[0], tt.a[1]) == codeCacheKey(tt.b[0], tt.b[1])
			if got != tt.wantSameAs {
				t.Errorf("same key = %v, want %v", got, tt.wantSameAs)
			}
		})
	}
}

func TestRendererCachesHighlightedBlocks(t *testing.T) {
	tests := []struct {
		name        string
		bodies      []string
		wantEntries int
	}{
		{"one block read twice", []string{fenced("go", "func a() {}\n"), fenced("go", "func a() {}\n")}, 1},
		{"an alias shares the entry", []string{fenced("go", "func a() {}\n"), fenced("golang", "func a() {}\n")}, 1},
		{"another code", []string{fenced("go", "func a() {}\n"), fenced("go", "func b() {}\n")}, 2},
		{"another language", []string{fenced("go", "x\n"), fenced("python", "x\n")}, 2},
		{"a plain block is not cached", []string{fenced("nosuchlang", "x\n"), fenced("", "x\n")}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := newCodeCache(codeCacheBytes, codeCacheEntryBytes)
			r := newRenderer(cache)
			var first string
			for i, b := range tt.bodies {
				out, err := r.Render(FormatMarkdown, b)
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = string(out)
				}
			}
			if len(cache.entries) != tt.wantEntries {
				t.Errorf("entries = %d, want %d", len(cache.entries), tt.wantEntries)
			}
			// A hit is the same HTML as the miss and as a renderer with no entry.
			again, err := r.Render(FormatMarkdown, tt.bodies[0])
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := newRenderer(newCodeCache(0, 0)).Render(FormatMarkdown, tt.bodies[0])
			if err != nil {
				t.Fatal(err)
			}
			if string(again) != first || string(fresh) != first {
				t.Errorf("a hit differs:\nfirst %s\nagain %s\nfresh %s", first, again, fresh)
			}
		})
	}
}

func TestRendererCacheStaysWithinTheLimit(t *testing.T) {
	const limit = 4 << 10
	cache := newCodeCache(limit, limit)
	r := newRenderer(cache)
	for i := range 200 {
		body := fenced("go", fmt.Sprintf("func f%d() { return %d }\n", i, i*7919))
		if _, err := r.Render(FormatMarkdown, body); err != nil {
			t.Fatal(err)
		}
		if cache.bytes > limit {
			t.Fatalf("after %d blocks the cache holds %d bytes, limit %d", i+1, cache.bytes, limit)
		}
	}
	if cache.bytes == 0 || len(cache.entries) == 0 || len(cache.entries) >= 200 {
		t.Errorf("entries = %d, bytes = %d: expected some, and some evicted", len(cache.entries), cache.bytes)
	}
}

// TestRendererCacheConcurrent renders many blocks from many goroutines
// against a small cache, so that lookups, inserts and evictions race. Run it
// with -race.
func TestRendererCacheConcurrent(t *testing.T) {
	const limit = 3 << 10
	cache := newCodeCache(limit, limit)
	r := newRenderer(cache)
	bodies := make([]string, 12)
	want := make([]string, len(bodies))
	reference := newRenderer(newCodeCache(0, 0))
	for i := range bodies {
		bodies[i] = fenced("go", fmt.Sprintf("package p%d\n\nfunc f() int { return %d }\n", i, i))
		out, err := reference.Render(FormatMarkdown, bodies[i])
		if err != nil {
			t.Fatal(err)
		}
		want[i] = string(out)
	}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range 100 {
				i := (g + n) % len(bodies)
				out, err := r.Render(FormatMarkdown, bodies[i])
				if err != nil || string(out) != want[i] {
					select {
					case errs <- fmt.Sprintf("goroutine %d, body %d: %v", g, i, err):
					default:
					}
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.bytes > limit {
		t.Errorf("the cache holds %d bytes, limit %d", cache.bytes, limit)
	}
}
