# Tên model hiện tại và cấp suy nghĩ

Các tên dưới đây chọn model/cấp suy nghĩ trong giao thức Gemini Web. Chúng là
tên của proxy này, không phải ID dùng với Google Gemini API chính thức.

| Chế độ | Thấp | Trung bình | Cao |
|---|---|---|---|
| Hydrogen | `gemini-4-hydrogen-low` | `gemini-4-hydrogen-medium` | `gemini-4-hydrogen-high` |
| Neon | `gemini-4-neon-low` | `gemini-4-neon-medium` | `gemini-4-neon-high` |
| Tự động | `gemini-auto` — không có hậu tố cấp suy nghĩ | — | — |

`gemini-4-hydrogen` và `gemini-4-neon` mặc định dùng mức Thấp.
`gemini-4-hydrogen-thinking` và `gemini-4-neon-thinking` là bí danh của mức Cao.
Các tên cũ như `gemini-3.8-flash` và `gemini-3.1-pro` vẫn nhận để cấu hình cũ
tiếp tục hoạt động; danh sách OpenAI `/v1/models` ưu tiên các tên hiện tại.

Ví dụ thân yêu cầu gửi đến `/v1/chat/completions`:

```json
{
  "model": "gemini-4-neon-high",
  "messages": [{"role": "user", "content": "Kiểm tra thuật toán này giúp tôi"}],
  "stream": true
}
```

Base URL và API key hiện có giữ nguyên. Không cần lấy lại cookie hoặc cài lại
extension để dùng tên model mới.

## Chi tiết triển khai và xác minh

- Hydrogen dùng mode `1`, ID `56fdd199312815e2`.
- Neon dùng mode `3`, ID `e6fa609c3fa255c0`.
- Thấp/Trung bình/Cao được mã hóa thành `1`/`4`/`2` ở cả trường payload
  `inner[80]` và trường header model `[15]`, kể cả khi nối tiếp cuộc trò chuyện.
- Tuyến Auto hiện dùng mode `6`; lần thử thực tế trả về metadata `3.5 Flash-Lite`.
  Không quảng cáo Auto luôn chọn một model cao cấp.
- Việc có `reasoning_content` phụ thuộc phản hồi thực tế; không tự tạo nội dung
  suy nghĩ hoặc đoán cấp suy nghĩ từ tốc độ trả lời.
- Kiểm tra cấp suy nghĩ bằng metadata model do Google trả về, không bằng việc
  hỏi mô hình tự nhận dạng hoặc chỉ nhìn trường `model` được API lặp lại.

Nguồn đối chiếu giao thức:

- https://github.com/zexadev/gemini-web2api-go/blob/main/internal/app/gemini.go
- https://github.com/zexadev/gemini-web2api-go/blob/main/CHANGELOG.md

Kết quả gọi thật được lưu riêng trong `artifacts/thinking-live-results.json` ở
workspace triển khai. Chỉ các mức đã được xác minh mới được báo cáo hoàn tất.
