# Request Prompts: xem "ai đã hỏi gì" trên Monitoring

- Ngày: 2026-10-14
- Trạng thái: chờ review

## 1. Mục tiêu

Người quản trị cần biết **từng người dùng (theo API key) đã hỏi gì, lúc nào**.

Luồng mong muốn:

1. Vào **Monitoring**, chọn time range và lọc theo **API key** (bộ lọc đã có sẵn).
2. Bảng realtime phía dưới (đã là danh sách từng call, đã theo đúng bộ lọc) hiện các call của key đó.
3. Bấm **"Xem câu hỏi"** ở một dòng để đọc câu người đó đã gõ.

Thành công khi: chọn một key + một ngày bất kỳ trong thời hạn lưu là đọc được câu hỏi, không phải tải file hay nhấn giữ dòng log.

## 2. Định nghĩa "câu hỏi"

**Câu hỏi = đoạn text người dùng tự gõ trong tin nhắn `user` mới nhất có text do người gõ.**

Client chính là Claude Code: một lượt hỏi sinh nhiều request, và ở phần lớn request tin `user` cuối chỉ là `tool_result`. Vì vậy khi trích:

- Duyệt các message từ cuối lên, lấy message `role: user` đầu tiên có ít nhất một block text "do người gõ".
- Bỏ block `tool_result`.
- Xoá các thẻ tự động khỏi text: `<system-reminder>`, `<command-name>`, `<command-message>`, `<command-args>`, `<local-command-stdout>`, `<local-command-stderr>`, `<local-command-caveat>` (xoá cả thẻ lẫn nội dung bên trong). Block nào sau khi xoá chỉ còn khoảng trắng thì bỏ.
- Block ảnh thay bằng `[image]`.
- Nối các block text còn lại bằng dòng trống, cắt tối đa **8 KB** (cắt theo rune UTF-8, có đánh dấu `…[truncated]`).

Hệ quả có chủ đích: nhiều call trong cùng một lượt Claude Code hiện **cùng một câu hỏi**.

Định dạng body hỗ trợ:

| Endpoint | Nơi lấy |
|---|---|
| Anthropic `/v1/messages` | `messages[]`, `content` là string hoặc mảng block |
| OpenAI `/v1/chat/completions` | `messages[]` role `user` |
| OpenAI `/v1/responses` | `input` (string, hoặc mảng item role `user`) |
| Gemini `…:generateContent` | `contents[]` role `user`, `parts[].text` |

Không nhận diện được → status `unparsable`, UI báo "Không đọc được câu hỏi".

## 3. Nguồn dữ liệu

CLIProxyAPI gắn cùng một `request_id` cho:

- usage event đẩy vào usage queue (`internal/redisqueue/plugin.go`, `GetRequestID(ctx)`), mà usage-service đã lưu vào `usage_events.request_id`;
- file request log, đọc được qua `GET /v0/management/request-log-by-id/{id}`.

File request log có các section `=== REQUEST INFO ===`, `=== HEADERS ===`, `=== REQUEST BODY ===`, `=== RESPONSE ===`. Body JSON nằm trong section `REQUEST BODY`.

**Điều kiện vận hành:** CLIProxyAPI phải bật `request-log: true` và `commercial-mode: false` (commercial mode chặn request log). Nếu không, mọi item sẽ thành `unavailable`.

Cách này bao phủ **mọi** traffic, kể cả gọi thẳng `cliproxy.omelet.tech` không qua CPA-Manager.

## 4. Thiết kế backend (usage-service)

### 4.1 Bảng mới

```sql
create table if not exists request_prompts (
  request_id      text primary key,
  status          text not null,          -- pending | ok | unavailable | unparsable
  question        text,
  attempts        integer not null default 0,
  next_attempt_ms integer not null,
  event_ts_ms     integer not null,       -- timestamp của usage event, dùng cho retention
  updated_at_ms   integer not null
);
create index if not exists idx_request_prompts_pending on request_prompts(status, next_attempt_ms);
create index if not exists idx_request_prompts_event_ts on request_prompts(event_ts_ms);
```

### 4.2 Enqueue

Trong `collector.Manager.processItems`, sau `InsertEvents` thành công: với mỗi event có `RequestID` khác rỗng, `insert or ignore` một dòng `pending` (`next_attempt_ms = now + 3s`, để CLIProxyAPI kịp ghi file). Lỗi enqueue chỉ log, không làm hỏng việc lưu usage.

Event nhập qua `/usage/import` **không** enqueue (log cũ không còn).

### 4.3 Worker

Package mới `internal/prompts`:

- `extract.go`: hàm thuần `ExtractQuestion(logFile []byte) (question string, status string)` — tách section `REQUEST BODY`, parse JSON, áp dụng quy tắc mục 2. Toàn bộ quy tắc nằm ở đây để test độc lập.
- `worker.go`: vòng lặp chạy cùng vòng đời collector (`Start`/`Stop`), dùng chung CPA URL + management key với collector.
  - Mỗi tick (2s) lấy tối đa 5 item `pending` có `next_attempt_ms <= now`, xử lý **tuần tự**.
  - Gọi `request-log-by-id/{id}`, timeout 30s, giới hạn đọc body 20 MB.
  - `200` → `ExtractQuestion` → `ok` hoặc `unparsable`.
  - `404` hoặc lỗi mạng → `attempts++`, backoff 5s, 15s, 45s, 2m, 5m; quá 5 lần → `unavailable`.
  - `401/403` → dừng tick, ghi lỗi vào status collector, thử lại tick sau (không đốt attempts).
- Retention: mỗi giờ xoá dòng có `event_ts_ms < now - retention`. Mặc định **30 ngày**, đổi qua env `USAGE_PROMPT_RETENTION_DAYS` (0 = tắt tính năng: không enqueue, worker không chạy).

### 4.4 API

- `GET /v0/management/usage`: mỗi detail thêm field `request_id` (đã có trong `usage.Event`, chỉ cần đưa vào `Detail`).
- Mới: `GET /v0/management/request-prompts/{request_id}` → `{ "request_id", "status", "question" }`. Không có dòng → `404 { "status": "not_found" }`. Yêu cầu management key như các API management khác (`authorizeIfConfigured`). Route phải đặt trước nhánh proxy `/v0/management/` trong `handleRoot`.

## 5. Thiết kế frontend

- `utils/usage`: `collectUsageDetailsWithEndpoint` giữ `request_id`; `MonitoringEventRow` thêm `requestId`.
- `usageServiceApi.getRequestPrompt(base, key, requestId)`.
- Bảng realtime (`MonitoringCenterPage`): thêm cột hành động, nút 💬 "Xem câu hỏi" chỉ hiện khi dòng có `requestId`.
- Bấm → modal (component riêng `RequestPromptModal`) gọi API khi mở, hiển thị:
  - đầu modal: thời gian, alias/mask API key, model, tokens;
  - thân: câu hỏi dạng text thuần (`white-space: pre-wrap`, không render HTML/markdown), nút Copy;
  - trạng thái: đang tải / `pending` "Đang lấy câu hỏi, thử lại sau ít phút" (nút Thử lại) / `unavailable` "Không còn request log cho call này" / `unparsable` / `not_found` "Call này không có câu hỏi (trước khi bật tính năng hoặc tính năng đang tắt)".
- i18n: thêm key cho en, zh-CN, zh-TW, ru (chuỗi tiếng Anh cho các ngôn ngữ chưa dịch, theo cách repo đang làm).

## 6. Bảo mật & quyền riêng tư

- Nội dung câu hỏi chỉ trả qua API cần management key; không log nội dung ra stdout.
- Câu hỏi có thể chứa dữ liệu riêng tư → nên thông báo cho người dùng rằng câu hỏi được lưu 30 ngày.
- Chỉ lưu câu hỏi đã cắt 8 KB, không lưu toàn bộ hội thoại hay câu trả lời.

## 7. Ngoài phạm vi (YAGNI)

- Lưu câu trả lời của model.
- Tìm kiếm full-text trong câu hỏi.
- Lấy câu hỏi cho call trước thời điểm deploy.
- Xem câu hỏi ngoài trang Monitoring.

## 8. Kiểm thử

- Go `prompts/extract_test.go`: Anthropic string content, block text + system-reminder, tin cuối là tool_result (phải lấy tin user trước đó), ảnh → `[image]`, OpenAI chat, OpenAI responses, Gemini, body hỏng → `unparsable`, cắt 8 KB đúng rune.
- Go `store`: enqueue idempotent, lấy pending theo `next_attempt_ms`, cập nhật trạng thái, retention xoá đúng.
- Go `worker`: upstream giả (`httptest`) trả 200 / 404-rồi-200 / 404 mãi / 401 → trạng thái đúng.
- Go `httpapi`: `/request-prompts/{id}` có/không key, có/không dòng; `/usage` có `request_id`.
- Frontend vitest: map `request_id` vào row; modal hiển thị đúng từng status.
- Chạy toàn bộ `go test ./...`, `npm run type-check`, eslint, `npm test`.

## 9. Triển khai

- Deploy CPA-Manager (auto từ `main`).
- Giữ CLIProxyAPI `request-log: true`, `commercial-mode: false`. Cân nhắc nâng `logs-max-total-size-mb` nếu worker hay gặp `unavailable` do file bị xoay vòng trước khi kịp đọc (worker đọc trong vài giây nên 100 MB thường đủ).
