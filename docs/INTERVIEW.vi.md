# Ghi chú để tự giải thích và sửa bài khi phỏng vấn

Hãy tự chạy demo, đọc code và thử sửa trước khi nộp. README đã khai báo việc dùng AI. Đề yêu cầu giải thích và làm thay đổi liên quan **không dùng AI** trong buổi phỏng vấn.

## Những điểm cần hiểu

- `model.go`: định danh sản phẩm, version, so sánh nội dung và kiểm tra dữ liệu. Giá dùng số nguyên theo đơn vị nhỏ nhất của tiền tệ.
- `store.go`: bbolt lưu xuống đĩa. `commitPage` ghi sản phẩm, audit, vấn đề và cursor trong **cùng transaction**. Nếu lỗi trước commit thì tất cả thay đổi của lô đó bị hủy.
- `engine.go`: đọc cursor đã lưu, lấy lô qua HTTP rồi commit. Khi ứng dụng chết, lần chạy tiếp đọc lại cursor này. Không dựa vào biến trong RAM.
- `supplier/http.go`: timeout, giới hạn kích thước, kiểm tra JSON và bắt buộc có dấu kết thúc rõ ràng.
- Không xóa sản phẩm đã rút bán; giữ tombstone cùng version. Ví dụ LAMP v4 rút bán, sau đó nhận LAMP v2 đang bán thì bỏ qua v2. Một v5 đang bán được coi là nhà cung cấp chủ động mở bán lại.
- Version bằng nhau nhưng nội dung khác nhau: giữ dữ liệu đã chấp nhận, báo conflict. Không có đủ thông tin để chọn bản nào đúng; yêu cầu version cao hơn.
- Đọc hết các lô nhưng có dữ liệu bị từ chối là `finished_with_issues`. Chỉ `completed` khi hết lô và không còn vấn đề mở. `finished_at` ghi lúc đọc hết feed, kể cả khi còn vấn đề.
- Nhập lại update tạo một bản ghi run mới để theo dõi lần thử, nhưng không tạo thêm identity sản phẩm hay thay đổi dữ liệu đã mới hơn. Run ID là mã nội bộ, update ID là mã feed do supplier cung cấp.
- HTTP lỗi làm dừng cả lô; dữ liệu từng sản phẩm sai thì được cách ly để những sản phẩm đúng trong cùng lô vẫn được nhập. Đây là quyết định thiết kế cần giải thích rõ.

## Tự luyện thay đổi nhỏ

1. Thêm trường `category` vào Record, validation, so sánh nội dung, fixture và test. Lưu ý field mới có thể ảnh hưởng replay của dữ liệu cũ.
2. Thêm bộ lọc sản phẩm theo trạng thái hoặc khoảng giá ở CLI.
3. Thêm thử lại HTTP 503 có giới hạn. Dùng context, không retry validation error, và bảo đảm không tăng cursor trước commit.
4. Thêm command chỉ in tổng số active/withdrawn products.

Chạy `go test ./...` và `go vet ./...` sau mỗi thay đổi. Tự viết một test có ý nghĩa trước hoặc cùng với thay đổi. Nếu chưa giải thích được một đoạn code, đọc tài liệu Go/bbolt và tự thử ví dụ nhỏ.

## Hạn chế có chủ đích

Một process CLI giữ database nên không xem status đồng thời khi import đang chạy. Không có nhà cung cấp thật, lịch chạy, login nhân viên, cơ chế backup online hay tự retry. Feed phải giữ nguyên nội dung và cursor trong suốt một update. Xem README và DESIGN.md để biết giả định đầy đủ.
