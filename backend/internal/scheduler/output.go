package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"smartpipe/backend/internal/codexrunner"
)

// Codex 输出条目类型。前端按这些稳定类型选择卡片，不依赖 app-server 的 wire type。
const (
	OutputKindAgentMessage = "agent_message"
	OutputKindPlan         = "plan"
	OutputKindReasoning    = "reasoning_summary"
	OutputKindCommand      = "command"
	OutputKindFileChange   = "file_change"
	OutputKindTool         = "tool"
	OutputKindSystem       = "system"
	OutputKindTokenUsage   = "token_usage"
)

const (
	// 与 Syntropy 桌面端一致：超过 64 KiB 的正文落盘，时间线只保留首尾各 4 KiB。
	outputExternalizeThreshold = 64 << 10
	outputHeadBytes            = 4 << 10
	outputTailBytes            = 4 << 10
	outputMaxBytes             = 4 << 20
	outputFlushInterval        = 350 * time.Millisecond
	outputBlobWriteInterval    = 1500 * time.Millisecond
	outputPageBytes            = 256 << 10
)

var (
	ansiEscapeRE = regexp.MustCompile("\x1b\\][^\x07]*(?:\x07|\x1b\\\\)|\x1b\\[[0-?]*[ -/]*[@-~]|\x1b[@-_]")
	secretREs    = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s]+`),
		regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password)\s*[=:]\s*)[^\s"']+`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}\b`),
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{16,}\b`),
	}
)

// outputBlobStore 保存被时间线摘要替代的大输出。写入先落临时文件再 rename，
// 读取侧不会看到半个版本；路径只接受单段 worker/item 名，避免目录穿越。
type outputBlobStore struct {
	root string
}

func newOutputBlobStore(root string) *outputBlobStore {
	root = strings.TrimSpace(root)
	if root == "" {
		root = filepath.Join(os.TempDir(), "smartpipe-scheduler-outputs")
	}
	return &outputBlobStore{root: root}
}

func safeOutputSegment(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}

func (b *outputBlobStore) path(workerID, itemID string) (string, error) {
	if b == nil || b.root == "" {
		return "", errors.New("scheduler: output blob store is not configured")
	}
	if !safeOutputSegment(workerID) || !safeOutputSegment(itemID) {
		return "", fmt.Errorf("scheduler: unsafe output key %q/%q", workerID, itemID)
	}
	return filepath.Join(b.root, workerID, itemID+".log"), nil
}

func (b *outputBlobStore) put(workerID, itemID, text string) (string, error) {
	target, err := b.path(workerID, itemID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	temp := target + ".tmp"
	if err := os.WriteFile(temp, []byte(text), 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temp, target); err != nil {
		_ = os.Remove(temp)
		return "", err
	}
	return workerID + "/" + itemID, nil
}

func (b *outputBlobStore) read(workerID, itemID string, offset, limit int64) (OutputRawPage, error) {
	page := OutputRawPage{}
	if offset < 0 {
		return page, errors.New("scheduler: output offset must not be negative")
	}
	if limit <= 0 || limit > outputPageBytes {
		limit = outputPageBytes
	}
	target, err := b.path(workerID, itemID)
	if err != nil {
		return page, err
	}
	file, err := os.Open(target)
	if errors.Is(err, os.ErrNotExist) {
		page.Missing = true
		return page, nil
	}
	if err != nil {
		return page, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return page, err
	}
	total := info.Size()
	if offset > total {
		offset = total
	}
	size := limit
	if total-offset < size {
		size = total - offset
	}
	buf := make([]byte, size)
	n, err := file.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return page, err
	}
	for n > 0 && !utf8.Valid(buf[:n]) {
		n--
	}
	page.Data = string(buf[:n])
	page.Offset = offset
	page.NextOffset = offset + int64(n)
	page.TotalBytes = total
	page.HasMore = page.NextOffset < total
	return page, nil
}

type outputItemState struct {
	out        WorkerOutput
	content    string
	output     string
	metadata   map[string]any
	lastBlobAt time.Time
	seq        int64
}

type workerOutputCollector struct {
	mu       sync.Mutex
	workerID string
	round    int
	turnID   string
	repo     Repo
	blobs    *outputBlobStore
	items    map[string]*outputItemState
	order    []string
	dirty    map[string]bool
	nextSeq  int64
	notice   int64
	stop     chan struct{}
	done     chan struct{}
	now      func() time.Time
	closed   bool
}

func newWorkerOutputCollector(repo Repo, blobs *outputBlobStore, workerID string, round int) *workerOutputCollector {
	c := &workerOutputCollector{
		workerID: workerID,
		round:    round,
		repo:     repo,
		blobs:    blobs,
		items:    map[string]*outputItemState{},
		dirty:    map[string]bool{},
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		now:      time.Now,
	}
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(outputFlushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.flush(false)
			case <-c.stop:
				return
			}
		}
	}()
	return c
}

func (c *workerOutputCollector) handle(ev codexrunner.Event) {
	if c == nil || strings.TrimSpace(ev.Method) == "" {
		return
	}
	var params map[string]any
	if len(ev.Params) > 0 {
		if err := json.Unmarshal(ev.Params, &params); err != nil {
			return
		}
	}
	if params == nil {
		params = map[string]any{}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	if turnID := stringAny(params["turnId"]); turnID != "" {
		c.turnID = turnID
	}

	switch ev.Method {
	case "turn/started":
		return
	case "turn/completed", "turn/failed":
		status := "completed"
		if ev.Method == "turn/failed" {
			status = "failed"
		}
		c.finishRunningLocked(status)
		return
	case "item/started", "item/completed":
		seed, ok := outputSeedFromParams(params, ev.Method == "item/completed")
		if ok {
			c.mergeSeedLocked(seed)
		}
	case "item/agentMessage/delta":
		c.appendDeltaLocked(stringAny(params["itemId"]), OutputKindAgentMessage, "content", stringAny(params["delta"]), "Agent 回复")
	case "item/plan/delta":
		c.appendDeltaLocked(stringAny(params["itemId"]), OutputKindPlan, "content", stringAny(params["delta"]), "计划")
	case "item/reasoning/summaryTextDelta":
		c.appendDeltaLocked(stringAny(params["itemId"]), OutputKindReasoning, "content", stringAny(params["delta"]), "推理摘要")
	case "item/reasoning/summaryPartAdded":
		c.appendDeltaLocked(stringAny(params["itemId"]), OutputKindReasoning, "content", "\n\n", "推理摘要")
	case "item/reasoning/textDelta":
		// 原始思维链不进入用户可见时间线；只投影 summary。
		return
	case "item/commandExecution/outputDelta":
		c.appendDeltaLocked(stringAny(params["itemId"]), OutputKindCommand, "output", stringAny(params["delta"]), "命令输出")
	case "item/fileChange/outputDelta":
		c.appendDeltaLocked(stringAny(params["itemId"]), OutputKindFileChange, "output", stringAny(params["delta"]), "文件变更")
	case "item/fileChange/patchUpdated":
		if seed, ok := fileChangeSeed(params, "running"); ok {
			c.mergeSeedLocked(seed)
		}
	case "turn/plan/updated":
		c.handlePlanUpdateLocked(params)
	case "turn/diff/updated":
		c.handleTurnDiffLocked(params)
	case "thread/tokenUsage/updated":
		c.handleTokenUsageLocked(params)
	case "item/mcpToolCall/progress":
		message := stringAny(params["message"])
		if message != "" {
			c.appendDeltaLocked(stringAny(params["itemId"]), OutputKindTool, "content", message+"\n", "工具调用")
		}
	case "item/commandExecution/terminalInteraction":
		if text := stringAny(params["stdin"]); text != "" {
			c.appendDeltaLocked(stringAny(params["itemId"]), OutputKindCommand, "output", "\n[stdin] "+text+"\n", "命令输出")
		}
	case "error", "warning", "configWarning", "guardianWarning", "deprecationNotice", "model/rerouted":
		c.handleNoticeLocked(ev.Method, params)
	}
}

type outputSeed struct {
	itemID   string
	kind     string
	status   string
	title    string
	content  string
	output   string
	metadata map[string]any
}

func (c *workerOutputCollector) itemLocked(itemID, kind, title string) *outputItemState {
	if strings.TrimSpace(itemID) == "" {
		c.notice++
		itemID = "scheduler-notice-" + strconv.FormatInt(c.notice, 10)
	}
	item := c.items[itemID]
	if item != nil {
		return item
	}
	c.nextSeq++
	item = &outputItemState{
		out: WorkerOutput{
			WorkerID: c.workerID, ItemID: itemID, Round: c.round, TurnID: c.turnID,
			Kind: kind, Status: defaultOutputStatus(kind), Title: title,
		},
		metadata: map[string]any{},
		seq:      c.nextSeq,
	}
	c.items[itemID] = item
	c.order = append(c.order, itemID)
	return item
}

func defaultOutputStatus(kind string) string {
	switch kind {
	case OutputKindCommand, OutputKindFileChange, OutputKindTool:
		return "running"
	default:
		return "streaming"
	}
}

func (c *workerOutputCollector) touchLocked(item *outputItemState) {
	c.dirty[item.out.ItemID] = true
}

func (c *workerOutputCollector) mergeSeedLocked(seed outputSeed) {
	if seed.itemID == "" || seed.kind == "" {
		return
	}
	item := c.itemLocked(seed.itemID, seed.kind, seed.title)
	if seed.kind != "" {
		item.out.Kind = seed.kind
	}
	if seed.title != "" {
		item.out.Title = seed.title
	}
	if seed.status != "" {
		item.out.Status = seed.status
	}
	// item/completed 的正文是权威快照，但只在它不短于已收到的 delta 时替换，
	// 避免服务端省略 aggregatedOutput 时把流式内容清空。
	if seed.content != "" && len(seed.content) >= len(item.content) {
		item.content = limitMiddle(cleanOutput(seed.content), outputMaxBytes)
	}
	if seed.output != "" && len(seed.output) >= len(item.output) {
		item.output = limitMiddle(cleanOutput(seed.output), outputMaxBytes)
	}
	for key, value := range seed.metadata {
		item.metadata[key] = value
	}
	c.touchLocked(item)
}

func (c *workerOutputCollector) appendDeltaLocked(itemID, kind, field, delta, title string) {
	if strings.TrimSpace(itemID) == "" || delta == "" {
		return
	}
	item := c.itemLocked(itemID, kind, title)
	item.out.Kind = kind
	delta = cleanOutput(delta)
	switch field {
	case "output":
		item.output = appendLimited(item.output, delta, outputMaxBytes)
		if item.output == "" {
			item.out.Truncated = true
		}
	default:
		item.content = appendLimited(item.content, delta, outputMaxBytes)
	}
	c.touchLocked(item)
}

func (c *workerOutputCollector) finishRunningLocked(status string) {
	for _, id := range c.order {
		item := c.items[id]
		if item == nil {
			continue
		}
		switch item.out.Status {
		case "running", "streaming", "pending", "":
			item.out.Status = status
			c.touchLocked(item)
		}
	}
}

func (c *workerOutputCollector) handlePlanUpdateLocked(params map[string]any) {
	steps, _ := params["plan"].([]any)
	if len(steps) == 0 {
		return
	}
	explanation := stringAny(params["explanation"])
	allCompleted := true
	metadataSteps := make([]map[string]any, 0, len(steps))
	for _, raw := range steps {
		step, _ := raw.(map[string]any)
		text := stringAny(step["step"])
		status := stringAny(step["status"])
		if text == "" {
			continue
		}
		switch status {
		case "completed":
		case "inProgress":
			allCompleted = false
		default:
			allCompleted = false
		}
		metadataSteps = append(metadataSteps, map[string]any{"text": text, "status": status})
	}
	status := "running"
	if allCompleted {
		status = "completed"
	}
	turnID := firstNonEmpty(stringAny(params["turnId"]), c.turnID)
	metadata := map[string]any{"plan": metadataSteps}
	if explanation != "" {
		metadata["explanation"] = explanation
	}
	c.mergeSeedLocked(outputSeed{
		itemID: "codex-plan-" + turnID, kind: OutputKindPlan, status: status, title: "执行计划",
		metadata: metadata,
	})
}

func (c *workerOutputCollector) handleTurnDiffLocked(params map[string]any) {
	turnID := firstNonEmpty(stringAny(params["turnId"]), c.turnID)
	diff := stringAny(params["diff"])
	if turnID == "" || diff == "" {
		return
	}
	adds, dels := countDiffLines(diff)
	files := strings.Count(diff, "diff --git ")
	if files == 0 {
		files = 1
	}
	c.mergeSeedLocked(outputSeed{
		itemID: "codex-turn-diff-" + turnID, kind: OutputKindFileChange, status: "completed", title: "本轮变更",
		content: fmt.Sprintf("已修改 %d 个文件，+%d -%d", files, adds, dels), output: diff,
		metadata: map[string]any{"source": "turn_diff", "files_count": files, "additions": adds, "deletions": dels},
	})
}

func (c *workerOutputCollector) handleTokenUsageLocked(params map[string]any) {
	turnID := firstNonEmpty(stringAny(params["turnId"]), c.turnID)
	usage, _ := params["tokenUsage"].(map[string]any)
	if usage == nil {
		usage, _ = params["usage"].(map[string]any)
	}
	if usage == nil {
		return
	}
	total, _ := usage["total"].(map[string]any)
	last, _ := usage["last"].(map[string]any)
	content := fmt.Sprintf("输入 %s · 缓存 %s · 输出 %s · 推理 %s",
		numberText(total["inputTokens"]), numberText(total["cachedInputTokens"]),
		numberText(total["outputTokens"]), numberText(total["reasoningOutputTokens"]))
	c.mergeSeedLocked(outputSeed{
		itemID: "codex-usage-" + turnID, kind: OutputKindTokenUsage, status: "completed", title: "Token 用量",
		content: content, metadata: map[string]any{"usage": usage, "last": last, "model_context_window": usage["modelContextWindow"]},
	})
}

func (c *workerOutputCollector) handleNoticeLocked(method string, params map[string]any) {
	level := "info"
	message := ""
	detail := ""
	switch method {
	case "error":
		level = "error"
		if retry, _ := params["willRetry"].(bool); retry {
			level = "warning"
		}
		errObj, _ := params["error"].(map[string]any)
		message = stringAny(errObj["message"])
		detail = stringAny(errObj["additionalDetails"])
	case "warning":
		// Codex desktop 默认不展示泛化的长线程 warning，这里同样不落用户时间线。
		return
	case "guardianWarning":
		level = "warning"
		message = stringAny(params["message"])
	case "configWarning":
		level = "warning"
		message = strings.TrimSpace(strings.Join([]string{stringAny(params["summary"]), stringAny(params["details"])}, "\n"))
	case "deprecationNotice":
		message = strings.TrimSpace(strings.Join([]string{stringAny(params["summary"]), stringAny(params["details"])}, "\n"))
	case "model/rerouted":
		level = "warning"
		message = "模型已切换：" + firstNonEmpty(stringAny(params["fromModel"]), "未知") + " → " + firstNonEmpty(stringAny(params["toModel"]), "未知")
		detail = stringAny(params["reason"])
	}
	if strings.TrimSpace(message) == "" && strings.TrimSpace(detail) == "" {
		return
	}
	c.notice++
	status := "completed"
	if level == "error" {
		status = "failed"
	}
	c.mergeSeedLocked(outputSeed{
		itemID: fmt.Sprintf("codex-notice-%d-%d", c.now().UnixNano(), c.notice), kind: OutputKindSystem,
		status: status, title: "引擎提示", content: strings.TrimSpace(message + "\n" + detail),
		metadata: map[string]any{"level": level, "source": method},
	})
}

func (c *workerOutputCollector) flush(force bool) {
	c.mu.Lock()
	now := c.now()
	type pendingUpdate struct {
		id  string
		out WorkerOutput
	}
	updates := make([]pendingUpdate, 0)
	for _, id := range c.order {
		if !c.dirty[id] {
			continue
		}
		item := c.items[id]
		if item == nil {
			continue
		}
		item.out.TurnID = firstNonEmpty(item.out.TurnID, c.turnID)
		item.out.Bytes = len(item.content) + len(item.output)
		full := item.output
		if full == "" {
			full = item.content
		}
		if len(full) > outputExternalizeThreshold && c.blobs != nil {
			if force || item.lastBlobAt.IsZero() || now.Sub(item.lastBlobAt) >= outputBlobWriteInterval {
				if ref, err := c.blobs.put(c.workerID, item.out.ItemID, full); err == nil {
					item.out.BlobRef = ref
					item.lastBlobAt = now
				}
			}
		}
		item.out.Content = outputExcerpt(item.content)
		item.out.Output = outputExcerpt(item.output)
		item.out.Truncated = len(item.content) > outputExternalizeThreshold || len(item.output) > outputExternalizeThreshold
		if len(item.metadata) == 0 {
			item.out.Metadata = json.RawMessage(`{}`)
		} else if raw, err := json.Marshal(item.metadata); err == nil {
			item.out.Metadata = raw
		} else {
			item.out.Metadata = json.RawMessage(`{}`)
		}
		if item.out.CreatedAt.IsZero() {
			item.out.CreatedAt = now
		}
		item.out.UpdatedAt = now
		c.dirty[id] = false
		updates = append(updates, pendingUpdate{id: id, out: item.out})
	}
	c.mu.Unlock()

	for _, update := range updates {
		out := update.out
		if err := c.repo.UpsertWorkerOutput(context.Background(), &out); err != nil {
			c.mu.Lock()
			c.dirty[update.id] = true
			c.mu.Unlock()
			continue
		}
		c.mu.Lock()
		if item := c.items[update.id]; item != nil {
			item.out.Revision = out.Revision
			item.out.CreatedAt = out.CreatedAt
		}
		c.mu.Unlock()
	}
}

func (c *workerOutputCollector) close(status string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.finishRunningLocked(status)
	c.mu.Unlock()
	close(c.stop)
	<-c.done
	c.flush(true)
}

func outputExcerpt(text string) string {
	if len(text) <= outputExternalizeThreshold {
		return text
	}
	head := text[:utf8PrefixCut(text, outputHeadBytes)]
	tail := text[utf8SuffixStart(text, outputTailBytes):]
	return head + fmt.Sprintf("\n\n…（输出已外置，共 %d 字节）…\n\n", len(text)) + tail
}

func utf8PrefixCut(text string, limit int) int {
	if len(text) <= limit {
		return len(text)
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return limit
}

func utf8SuffixStart(text string, limit int) int {
	start := len(text) - limit
	if start <= 0 {
		return 0
	}
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return start
}

func appendLimited(base, delta string, limit int) string {
	if len(base) >= limit || delta == "" {
		return base
	}
	remaining := limit - len(base)
	if len(delta) > remaining {
		delta = delta[:utf8PrefixCut(delta, remaining)]
	}
	return base + delta
}

func limitMiddle(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	head := text[:utf8PrefixCut(text, limit/2)]
	tail := text[utf8SuffixStart(text, limit/2):]
	return head + "\n\n…（内容过长，已截断）…\n\n" + tail
}

func cleanOutput(text string) string {
	if text == "" {
		return text
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = ansiEscapeRE.ReplaceAllString(text, "")
	for _, re := range secretREs {
		text = re.ReplaceAllString(text, "$1[redacted]")
	}
	return text
}

func outputSeedFromParams(params map[string]any, completed bool) (outputSeed, bool) {
	item, ok := params["item"].(map[string]any)
	if !ok {
		return outputSeed{}, false
	}
	id := stringAny(item["id"])
	if id == "" {
		return outputSeed{}, false
	}
	wireType := stringAny(item["type"])
	kind := outputKindForWireType(wireType)
	if kind == "" {
		return outputSeed{}, false
	}
	status := normalizedOutputStatus(stringAny(item["status"]), completed, kind)
	seed := outputSeed{itemID: id, kind: kind, status: status, metadata: map[string]any{"wire_type": wireType}}
	switch kind {
	case OutputKindAgentMessage:
		seed.title = "Agent 回复"
		seed.content = stringAny(item["text"])
	case OutputKindPlan:
		seed.title = "计划"
		seed.content = stringAny(item["text"])
	case OutputKindReasoning:
		seed.title = "推理摘要"
		seed.content = joinAnyText(item["summary"])
	case OutputKindCommand:
		seed.title, seed.metadata["command_action_type"], seed.metadata["command_action_path"] = commandDisplay(item)
		if strings.TrimSpace(seed.title) == "" {
			seed.title = "执行命令"
		}
		seed.output = stringAny(item["aggregatedOutput"])
		copyOutputDetail(seed.metadata, item, "cwd", "durationMs", "exitCode", "processId")
	case OutputKindFileChange:
		seed.title = "文件变更"
		seed.output = stringAny(item["output"])
		if files, changes := outputFiles(item["changes"]); len(files) > 0 {
			seed.metadata["files"] = files
			if seed.content == "" {
				adds, dels := fileChangeTotals(changes)
				seed.content = fmt.Sprintf("已修改 %d 个文件，+%d -%d", len(files), adds, dels)
			}
		}
	case OutputKindTool:
		seed.title = firstNonEmpty(stringAny(item["tool"]), stringAny(item["name"]), stringAny(item["query"]), wireType)
		seed.content = compactAny(item["arguments"])
		seed.output = firstNonEmpty(stringAny(item["result"]), stringAny(item["output"]), stringAny(item["aggregatedOutput"]))
		copyOutputDetail(seed.metadata, item, "server", "namespace", "durationMs", "query", "savedPath")
	case OutputKindSystem:
		seed.title = "系统"
		seed.content = firstNonEmpty(stringAny(item["text"]), stringAny(item["message"]))
		seed.metadata["level"] = "info"
		seed.metadata["source"] = wireType
	}
	return seed, true
}

func outputKindForWireType(wireType string) string {
	switch wireType {
	case "agentMessage":
		return OutputKindAgentMessage
	case "plan", "todoList":
		return OutputKindPlan
	case "reasoning":
		return OutputKindReasoning
	case "commandExecution":
		return OutputKindCommand
	case "fileChange":
		return OutputKindFileChange
	case "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "subAgentActivity", "webSearch", "sleep", "imageGeneration", "functionCallOutput":
		return OutputKindTool
	case "hookPrompt", "contextCompaction", "worktreeInit", "remoteTaskCreated", "modelChanged", "personalityChanged", "enteredReviewMode", "exitedReviewMode":
		return OutputKindSystem
	case "userMessage":
		return ""
	default:
		return ""
	}
}

func normalizedOutputStatus(status string, completed bool, kind string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending":
		return "pending"
	case "inprogress", "in_progress", "running", "active":
		return "running"
	case "completed", "succeeded":
		return "completed"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled", "interrupted":
		return "cancelled"
	case "denied", "declined":
		return "denied"
	}
	if completed {
		return "completed"
	}
	return defaultOutputStatus(kind)
}

func commandDisplay(item map[string]any) (string, string, string) {
	actions, _ := item["commandActions"].([]any)
	for i := len(actions) - 1; i >= 0; i-- {
		action, _ := actions[i].(map[string]any)
		command := stringAny(action["command"])
		if command != "" {
			return command, stringAny(action["type"]), firstNonEmpty(stringAny(action["path"]), stringAny(action["name"]))
		}
	}
	return stringAny(item["command"]), "", ""
}

func outputFiles(value any) ([]map[string]any, []map[string]any) {
	raw, _ := value.([]any)
	files := make([]map[string]any, 0, len(raw))
	changes := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		change, _ := entry.(map[string]any)
		path := stringAny(change["path"])
		if path == "" {
			continue
		}
		diff := stringAny(change["diff"])
		adds, dels := countDiffLines(diff)
		file := map[string]any{
			"path": path, "kind": stringAny(change["kind"]), "additions": adds, "deletions": dels,
			"diff": limitMiddle(diff, 128<<10),
		}
		files = append(files, file)
		changes = append(changes, file)
	}
	return files, changes
}

func fileChangeTotals(files []map[string]any) (int, int) {
	adds, dels := 0, 0
	for _, file := range files {
		adds += intAny(file["additions"])
		dels += intAny(file["deletions"])
	}
	return adds, dels
}

func fileChangeSeed(params map[string]any, status string) (outputSeed, bool) {
	id := stringAny(params["itemId"])
	if id == "" {
		return outputSeed{}, false
	}
	files, changes := outputFiles(params["changes"])
	adds, dels := fileChangeTotals(changes)
	metadata := map[string]any{"wire_type": "fileChange"}
	if len(files) > 0 {
		metadata["files"] = files
	}
	return outputSeed{
		itemID: id, kind: OutputKindFileChange, status: status, title: "文件变更",
		content: fmt.Sprintf("已修改 %d 个文件，+%d -%d", len(files), adds, dels), metadata: metadata,
	}, true
}

func countDiffLines(diff string) (int, int) {
	adds, dels := 0, 0
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			adds++
		case strings.HasPrefix(line, "-"):
			dels++
		}
	}
	return adds, dels
}

func joinAnyText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		parts := make([]string, 0, len(typed))
		for _, part := range typed {
			if text := stringAny(part); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n\n")
	default:
		return ""
	}
}

func compactAny(value any) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return limitMiddle(string(raw), 128<<10)
}

func copyOutputDetail(dst map[string]any, src map[string]any, keys ...string) {
	for _, key := range keys {
		if value, ok := src[key]; ok && value != nil {
			dst[key] = value
		}
	}
}

func stringAny(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func intAny(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		n, _ := typed.Int64()
		return int(n)
	default:
		return 0
	}
}

func numberText(value any) string {
	switch typed := value.(type) {
	case float64:
		return strconv.FormatFloat(typed, 'f', 0, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	default:
		return "0"
	}
}

// WorkerOutputs 返回 worker 的当前输出快照，Cursor 可直接作为 SSE 的 after。
func (s *Scheduler) WorkerOutputs(ctx context.Context, id string, after int64, limit int) (*OutputFrame, error) {
	w, err := s.loadWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	items, err := s.repo.ListWorkerOutputs(ctx, id, after, limit)
	if err != nil {
		return nil, err
	}
	cursor := after
	for _, item := range items {
		if item.Revision > cursor {
			cursor = item.Revision
		}
	}
	return &OutputFrame{Cursor: cursor, Items: items, Running: s.isRunning(w.ID)}, nil
}

// WorkerOutputRaw 读取一条输出的完整正文；外置日志按偏移分页。
func (s *Scheduler) WorkerOutputRaw(ctx context.Context, workerID, itemID string, offset, limit int64) (*OutputRawPage, error) {
	item, err := s.repo.GetWorkerOutput(ctx, workerID, itemID)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, ErrWorkerNotFound
	}
	if strings.TrimSpace(item.BlobRef) != "" {
		page, err := s.outputBlobs.read(workerID, itemID, offset, limit)
		if err != nil {
			return nil, err
		}
		return &page, nil
	}
	text := item.Output
	if text == "" {
		text = item.Content
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > outputPageBytes {
		limit = outputPageBytes
	}
	if offset > int64(len(text)) {
		offset = int64(len(text))
	}
	end := offset + limit
	if end > int64(len(text)) {
		end = int64(len(text))
	}
	for end > offset && !utf8.ValidString(text[offset:end]) {
		end--
	}
	return &OutputRawPage{
		Data: text[offset:end], Offset: offset, NextOffset: end,
		TotalBytes: int64(len(text)), HasMore: end < int64(len(text)),
	}, nil
}

func (s *Scheduler) runWorkerTurn(ctx context.Context, w *Worker, spec codexrunner.Spec) (res *codexrunner.Result, err error) {
	collector := newWorkerOutputCollector(s.repo, s.outputBlobs, w.ID, w.Round)
	spec.OnEvent = collector.handle
	defer func() {
		status := "completed"
		if err != nil {
			status = "failed"
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				status = "cancelled"
			}
		} else if res != nil {
			switch strings.ToLower(res.Status) {
			case "error", "failed":
				status = "failed"
			case "cancelled", "canceled", "interrupted":
				status = "cancelled"
			}
		}
		if recovered := recover(); recovered != nil {
			collector.close("failed")
			panic(recovered)
		}
		collector.close(status)
	}()
	return s.runner.Run(ctx, spec)
}
