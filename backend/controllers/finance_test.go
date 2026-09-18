package controllers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"

	"edu-train/database"
	"edu-train/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupTestEnv(t *testing.T) *gin.Engine {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	// 串行化连接，避免 SQLite 并发写锁冲突；条件更新的原子性仍被验证
	sqlDB.SetMaxOpenConns(1)

	if err := db.AutoMigrate(
		&models.User{},
		&models.Student{},
		&models.Course{},
		&models.Payment{},
		&models.Refund{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	database.DB = db
	t.Cleanup(func() { database.DB = nil })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user_id", uint(1))
		c.Next()
	})
	r.GET("/payments", GetPayments)
	r.GET("/payments/:id", GetPayment)
	r.POST("/payments", CreatePayment)
	r.GET("/refunds", GetRefunds)
	r.POST("/refunds", CreateRefund)
	r.POST("/refunds/:id/process", ProcessRefund)
	r.GET("/finance/reports", GetFinanceReports)
	return r
}

func doRequest(t *testing.T, r *gin.Engine, method, path string, body interface{}) (int, map[string]interface{}) {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("%s %s: invalid json %q: %v", method, path, w.Body.String(), err)
	}
	return w.Code, resp
}

func respData(t *testing.T, resp map[string]interface{}) map[string]interface{} {
	t.Helper()
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("response data is not an object: %v", resp)
	}
	return data
}

func seedStudent(t *testing.T, name string) models.Student {
	t.Helper()
	student := models.Student{Name: name, Phone: "13800000000"}
	if err := database.DB.Create(&student).Error; err != nil {
		t.Fatalf("create student: %v", err)
	}
	return student
}

func createPayment(t *testing.T, r *gin.Engine, studentID uint, amount float64, method string) map[string]interface{} {
	t.Helper()
	_, resp := doRequest(t, r, "POST", "/payments", map[string]interface{}{
		"student_id":     studentID,
		"amount":         amount,
		"payment_method": method,
		"payment_date":   "2026-09-18",
		"type":           "tuition",
	})
	if resp["code"].(float64) != 0 {
		t.Fatalf("create payment failed: %v", resp)
	}
	return respData(t, resp)
}

func getPayment(t *testing.T, r *gin.Engine, id interface{}) map[string]interface{} {
	t.Helper()
	_, resp := doRequest(t, r, "GET", fmt.Sprintf("/payments/%v", id), nil)
	if resp["code"].(float64) != 0 {
		t.Fatalf("get payment failed: %v", resp)
	}
	return respData(t, resp)
}

func createRefundRequest(t *testing.T, r *gin.Engine, paymentID interface{}, amount float64) map[string]interface{} {
	t.Helper()
	_, resp := doRequest(t, r, "POST", "/refunds", map[string]interface{}{
		"payment_id": paymentID,
		"amount":     amount,
		"reason":     "测试退费",
	})
	return resp
}

func processRefund(t *testing.T, r *gin.Engine, id interface{}, status string) map[string]interface{} {
	t.Helper()
	_, resp := doRequest(t, r, "POST", fmt.Sprintf("/refunds/%v/process", id), map[string]interface{}{
		"status": status,
	})
	return resp
}

func assertAmount(t *testing.T, payment map[string]interface{}, key string, want float64) {
	t.Helper()
	got, ok := payment[key].(float64)
	if !ok {
		t.Fatalf("payment[%s] missing or not a number: %v", key, payment[key])
	}
	if got != want {
		t.Fatalf("payment[%s] = %v, want %v", key, got, want)
	}
}

func countRefunds(t *testing.T, paymentID interface{}) int64 {
	t.Helper()
	var n int64
	if err := database.DB.Model(&models.Refund{}).Where("payment_id = ?", paymentID).Count(&n).Error; err != nil {
		t.Fatalf("count refunds: %v", err)
	}
	return n
}

func TestRefundClosedLoop(t *testing.T) {
	r := setupTestEnv(t)
	student := seedStudent(t, "张三")
	payment := createPayment(t, r, student.ID, 1000, "wechat")
	pid := payment["id"]

	// 初始：可退=实收，净收入=实收
	p := getPayment(t, r, pid)
	assertAmount(t, p, "refundable_amount", 1000)
	assertAmount(t, p, "net_income", 1000)
	assertAmount(t, p, "pending_refund_amount", 0)
	assertAmount(t, p, "refunded_amount", 0)

	// 申请 400 + 500 = 900 <= 1000，均可提交
	resp := createRefundRequest(t, r, pid, 400)
	if resp["code"].(float64) != 0 {
		t.Fatalf("create refund 400 failed: %v", resp)
	}
	refund1 := respData(t, resp)["id"]

	resp = createRefundRequest(t, r, pid, 500)
	if resp["code"].(float64) != 0 {
		t.Fatalf("create refund 500 failed: %v", resp)
	}
	refund2 := respData(t, resp)["id"]

	// 待审+已退=900，再申请 200 超出实收，整单拒绝且不落库
	resp = createRefundRequest(t, r, pid, 200)
	if resp["code"].(float64) == 0 {
		t.Fatalf("expected rejection for exceeding refund, got: %v", resp)
	}
	if n := countRefunds(t, pid); n != 2 {
		t.Fatalf("rejected refund should not be persisted, refund count = %d", n)
	}

	p = getPayment(t, r, pid)
	assertAmount(t, p, "pending_refund_amount", 900)
	assertAmount(t, p, "refundable_amount", 100)
	assertAmount(t, p, "net_income", 1000)

	// 非法金额与非法状态
	if resp := createRefundRequest(t, r, pid, 0); resp["code"].(float64) == 0 {
		t.Fatalf("expected rejection for zero amount")
	}
	if resp := processRefund(t, r, refund1, "done"); resp["code"].(float64) == 0 {
		t.Fatalf("expected rejection for invalid process status")
	}

	// 批准第一笔 400
	resp = processRefund(t, r, refund1, "approved")
	if resp["code"].(float64) != 0 {
		t.Fatalf("approve refund1 failed: %v", resp)
	}
	if respData(t, resp)["refund_date"] == nil || respData(t, resp)["refund_date"] == "" {
		t.Fatalf("approved refund should have refund_date: %v", resp)
	}

	p = getPayment(t, r, pid)
	assertAmount(t, p, "refunded_amount", 400)
	assertAmount(t, p, "pending_refund_amount", 500)
	assertAmount(t, p, "refundable_amount", 100)
	assertAmount(t, p, "net_income", 600)

	// 重复处理同一申请必须失败，且金额不变
	resp = processRefund(t, r, refund1, "approved")
	if resp["code"].(float64) == 0 {
		t.Fatalf("expected rejection for duplicate process")
	}
	resp = processRefund(t, r, refund1, "rejected")
	if resp["code"].(float64) == 0 {
		t.Fatalf("expected rejection for re-process with different status")
	}
	p = getPayment(t, r, pid)
	assertAmount(t, p, "refunded_amount", 400)
	assertAmount(t, p, "net_income", 600)

	// 批准第二笔 500，已退合计 900
	resp = processRefund(t, r, refund2, "approved")
	if resp["code"].(float64) != 0 {
		t.Fatalf("approve refund2 failed: %v", resp)
	}
	p = getPayment(t, r, pid)
	assertAmount(t, p, "refunded_amount", 900)
	assertAmount(t, p, "refundable_amount", 100)
	assertAmount(t, p, "net_income", 100)

	// 剩余 100 可退：申请 100 成功，再申请 0.01 失败
	resp = createRefundRequest(t, r, pid, 100)
	if resp["code"].(float64) != 0 {
		t.Fatalf("create refund 100 failed: %v", resp)
	}
	refund3 := respData(t, resp)["id"]
	if resp := createRefundRequest(t, r, pid, 0.01); resp["code"].(float64) == 0 {
		t.Fatalf("expected rejection when pending+approved equals amount")
	}

	resp = processRefund(t, r, refund3, "approved")
	if resp["code"].(float64) != 0 {
		t.Fatalf("approve refund3 failed: %v", resp)
	}
	p = getPayment(t, r, pid)
	assertAmount(t, p, "refunded_amount", 1000)
	assertAmount(t, p, "refundable_amount", 0)
	assertAmount(t, p, "net_income", 0)
}

func TestRefundRejectFlow(t *testing.T) {
	r := setupTestEnv(t)
	student := seedStudent(t, "李四")
	payment := createPayment(t, r, student.ID, 800, "alipay")
	pid := payment["id"]

	resp := createRefundRequest(t, r, pid, 300)
	if resp["code"].(float64) != 0 {
		t.Fatalf("create refund failed: %v", resp)
	}
	refundID := respData(t, resp)["id"]

	// 驳回后：已退不变，可退恢复
	resp = processRefund(t, r, refundID, "rejected")
	if resp["code"].(float64) != 0 {
		t.Fatalf("reject refund failed: %v", resp)
	}
	if respData(t, resp)["status"] != "rejected" {
		t.Fatalf("expected status rejected, got: %v", respData(t, resp)["status"])
	}

	p := getPayment(t, r, pid)
	assertAmount(t, p, "refunded_amount", 0)
	assertAmount(t, p, "pending_refund_amount", 0)
	assertAmount(t, p, "refundable_amount", 800)
	assertAmount(t, p, "net_income", 800)

	// 已驳回不能再次处理
	if resp := processRefund(t, r, refundID, "approved"); resp["code"].(float64) == 0 {
		t.Fatalf("expected rejection when processing rejected refund")
	}

	// 驳回不占额度，可重新申请全额
	resp = createRefundRequest(t, r, pid, 800)
	if resp["code"].(float64) != 0 {
		t.Fatalf("create full refund after reject failed: %v", resp)
	}
}

func TestRefundConcurrentProcess(t *testing.T) {
	r := setupTestEnv(t)
	student := seedStudent(t, "王五")
	payment := createPayment(t, r, student.ID, 500, "cash")
	pid := payment["id"]

	resp := createRefundRequest(t, r, pid, 200)
	if resp["code"].(float64) != 0 {
		t.Fatalf("create refund failed: %v", resp)
	}
	refundID := respData(t, resp)["id"]

	// 并发处理同一申请，只能成功一次
	const workers = 8
	var wg sync.WaitGroup
	codes := make([]float64, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, resp := doRequest(t, r, "POST", fmt.Sprintf("/refunds/%v/process", refundID), map[string]interface{}{
				"status": "approved",
			})
			codes[idx] = resp["code"].(float64)
		}(i)
	}
	wg.Wait()

	succeeded := 0
	for _, code := range codes {
		if code == 0 {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("expected exactly 1 successful process, got %d (codes=%v)", succeeded, codes)
	}

	p := getPayment(t, r, pid)
	assertAmount(t, p, "refunded_amount", 200)
	assertAmount(t, p, "net_income", 300)
}

func TestRefundApproveFailureKeepsAmounts(t *testing.T) {
	r := setupTestEnv(t)
	student := seedStudent(t, "赵六")
	payment := createPayment(t, r, student.ID, 1000, "bank")
	pid := payment["id"]

	resp := createRefundRequest(t, r, pid, 600)
	if resp["code"].(float64) != 0 {
		t.Fatalf("create refund failed: %v", resp)
	}
	refundID := respData(t, resp)["id"]

	// 模拟异常数据：已退金额被外部改为 1000，批准 600 必然触发守卫失败
	if err := database.DB.Model(&models.Payment{}).Where("id = ?", pid).
		Update("refunded_amount", 1000).Error; err != nil {
		t.Fatalf("tamper refunded_amount: %v", err)
	}

	resp = processRefund(t, r, refundID, "approved")
	if resp["code"].(float64) == 0 {
		t.Fatalf("expected approve to fail when refundable is insufficient")
	}

	// 失败不得改变任何金额与状态
	p := getPayment(t, r, pid)
	assertAmount(t, p, "refunded_amount", 1000)
	assertAmount(t, p, "pending_refund_amount", 600)
	assertAmount(t, p, "net_income", 0)

	var refund models.Refund
	if err := database.DB.First(&refund, refundID).Error; err != nil {
		t.Fatalf("get refund: %v", err)
	}
	if refund.Status != "pending" {
		t.Fatalf("failed approve must keep refund pending, got %s", refund.Status)
	}
	if refund.ProcessedBy != nil {
		t.Fatalf("failed approve must not set processed_by")
	}
}

func TestFinanceReportsWithRefunds(t *testing.T) {
	r := setupTestEnv(t)
	student := seedStudent(t, "钱七")

	p1 := createPayment(t, r, student.ID, 1000, "wechat")
	createPayment(t, r, student.ID, 500, "alipay")

	resp := createRefundRequest(t, r, p1["id"], 400)
	if resp["code"].(float64) != 0 {
		t.Fatalf("create refund failed: %v", resp)
	}
	if resp := processRefund(t, r, respData(t, resp)["id"], "approved"); resp["code"].(float64) != 0 {
		t.Fatalf("approve refund failed: %v", resp)
	}

	_, resp = doRequest(t, r, "GET", "/finance/reports?type=daily", nil)
	if resp["code"].(float64) != 0 {
		t.Fatalf("get reports failed: %v", resp)
	}
	rows, ok := resp["data"].([]interface{})
	if !ok || len(rows) != 2 {
		t.Fatalf("expected 2 report rows (one per payment_method), got: %v", resp["data"])
	}

	byMethod := map[string]map[string]interface{}{}
	for _, row := range rows {
		m := row.(map[string]interface{})
		byMethod[m["payment_method"].(string)] = m
	}

	// 原收款方式统计保持有效，并新增已退与净收入
	wechat := byMethod["wechat"]
	if wechat == nil {
		t.Fatalf("missing wechat report row: %v", rows)
	}
	if wechat["total_income"].(float64) != 1000 {
		t.Fatalf("wechat total_income = %v, want 1000", wechat["total_income"])
	}
	if wechat["payment_count"].(float64) != 1 {
		t.Fatalf("wechat payment_count = %v, want 1", wechat["payment_count"])
	}
	if wechat["total_refund"].(float64) != 400 {
		t.Fatalf("wechat total_refund = %v, want 400", wechat["total_refund"])
	}
	if wechat["net_income"].(float64) != 600 {
		t.Fatalf("wechat net_income = %v, want 600", wechat["net_income"])
	}

	alipay := byMethod["alipay"]
	if alipay == nil {
		t.Fatalf("missing alipay report row: %v", rows)
	}
	if alipay["total_income"].(float64) != 500 || alipay["net_income"].(float64) != 500 {
		t.Fatalf("alipay row wrong: %v", alipay)
	}
}

func TestPaymentsListCarriesRefundDerived(t *testing.T) {
	r := setupTestEnv(t)
	student := seedStudent(t, "孙八")
	payment := createPayment(t, r, student.ID, 600, "cash")
	pid := payment["id"]

	resp := createRefundRequest(t, r, pid, 100)
	if resp["code"].(float64) != 0 {
		t.Fatalf("create refund failed: %v", resp)
	}
	if resp := processRefund(t, r, respData(t, resp)["id"], "approved"); resp["code"].(float64) != 0 {
		t.Fatalf("approve refund failed: %v", resp)
	}

	// 列表接口同样返回派生金额，刷新后保持一致
	_, resp = doRequest(t, r, "GET", "/payments", nil)
	if resp["code"].(float64) != 0 {
		t.Fatalf("get payments failed: %v", resp)
	}
	data := respData(t, resp)
	list, ok := data["list"].([]interface{})
	if !ok || len(list) != 1 {
		t.Fatalf("expected 1 payment in list, got: %v", data["list"])
	}
	p := list[0].(map[string]interface{})
	assertAmount(t, p, "refunded_amount", 100)
	assertAmount(t, p, "refundable_amount", 500)
	assertAmount(t, p, "net_income", 500)

	// 退费列表可按缴费单过滤并携带状态
	_, resp = doRequest(t, r, "GET", fmt.Sprintf("/refunds?payment_id=%v", pid), nil)
	if resp["code"].(float64) != 0 {
		t.Fatalf("get refunds failed: %v", resp)
	}
	refunds, ok := resp["data"].([]interface{})
	if !ok || len(refunds) != 1 {
		t.Fatalf("expected 1 refund, got: %v", resp["data"])
	}
	rf := refunds[0].(map[string]interface{})
	if rf["status"] != "approved" {
		t.Fatalf("refund status = %v, want approved", rf["status"])
	}
}
