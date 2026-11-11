package automation

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestLiveAISwap(t *testing.T) {
	var l LiveAI
	if l.Enabled() || l.Model() != "" {
		t.Fatal("初始應未啟用")
	}
	if _, err := l.Decide(context.Background(), Settings{}, nil, nil); !errors.Is(err, ErrAIDisabled) {
		t.Fatalf("Decide: %v", err)
	}
	if _, err := l.Caption(context.Background(), Settings{}, nil, "x", "image", nil); !errors.Is(err, ErrAIDisabled) {
		t.Fatalf("Caption: %v", err)
	}
	l.Set("sk-ant-test", "m1", "low")
	if !l.Enabled() || l.Model() != "m1" {
		t.Fatal("Set 後應啟用")
	}
	l.Set("sk-ant-test", "m2", "high")
	if l.Model() != "m2" {
		t.Fatal("換模型應立即生效")
	}
	l.Set("", "m2", "high")
	if l.Enabled() {
		t.Fatal("清掉 key 應停用")
	}
}

func TestEngineAIEnabled(t *testing.T) {
	if (&Engine{}).AIEnabled() {
		t.Fatal("AI=nil 應為未啟用")
	}
	l := &LiveAI{}
	e := &Engine{AI: l}
	if e.AIEnabled() {
		t.Fatal("LiveAI 沒 key 應為未啟用")
	}
	l.Set("k", "m", "low")
	if !e.AIEnabled() {
		t.Fatal("LiveAI 有 key 應啟用")
	}
	if !(&Engine{AI: &fakeAI{}}).AIEnabled() {
		t.Fatal("一般 Decider 視為啟用")
	}
}

func TestLiveAIConcurrentSet(t *testing.T) {
	var l LiveAI
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				l.Set([]string{"", "k"}[j%2], "m", "low")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = l.Enabled()
				_ = l.Model()
				_, _ = l.Decide(context.Background(), Settings{}, nil, nil)
			}
		}()
	}
	wg.Wait()
}
