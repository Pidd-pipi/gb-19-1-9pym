package controllers

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	"edu-train/database"
	"edu-train/models"
	"edu-train/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// refundEpsilon 用于金额比较时消除浮点误差
const refundEpsilon = 0.001

var (
	errRefundAmountInvalid  = errors.New("退费金额必须大于0")
	errRefundPaymentGone    = errors.New("缴费记录不存在或未完成收款，无法退费")
	errRefundStudentMismatch = errors.New("退费学员与缴费记录所属学员不一致")
	errRefundExceedPaid     = errors.New("待审与已退金额合计不得超过实收金额，已整单拒绝")
	errRefundNotPending     = errors.New("仅待审核的退费申请可以批准或驳回")
	errRefundActionInvalid  = errors.New("处理操作仅支持 approved 或 rejected")
)

func GetPayments(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	studentID := c.Query("student_id")
	paymentMethod := c.Query("payment_method")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")
	typeParam := c.Query("type")

	offset := (page - 1) * pageSize

	query := database.DB.Model(&models.Payment{}).Preload("Student").Preload("Course")

	if studentID != "" {
		query = query.Where("student_id = ?", studentID)
	}

	if paymentMethod != "" {
		query = query.Where("payment_method = ?", paymentMethod)
	}

	if startDate != "" {
		query = query.Where("payment_date >= ?", startDate)
	}

	if endDate != "" {
		query = query.Where("payment_date <= ?", endDate)
	}

	if typeParam != "" {
		query = query.Where("type = ?", typeParam)
	}

	var total int64
	query.Count(&total)

	var payments []models.Payment
	if err := query.Order("payment_date DESC, created_at DESC").Offset(offset).Limit(pageSize).Find(&payments).Error; err != nil {
		utils.InternalServerError(c, "查询失败")
		return
	}

	utils.Success(c, gin.H{
		"list":      payments,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func GetPayment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var payment models.Payment
	if err := database.DB.Preload("Student").Preload("Course").First(&payment, id).Error; err != nil {
		utils.NotFound(c, "缴费记录不存在")
		return
	}

	utils.Success(c, payment)
}

func CreatePayment(c *gin.Context) {
	var payment models.Payment
	if err := c.ShouldBindJSON(&payment); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}

	payment.ReceiptNo = generateReceiptNo()
	payment.Status = "paid"

	if payment.Type == "" {
		payment.Type = "tuition"
	}

	// 新缴费无退费：已退/待审为 0，净收入等于实收，可退金额等于实收
	payment.RefundedAmount = 0
	payment.PendingRefundAmount = 0
	payment.NetIncome = payment.Amount
	payment.AvailableRefundAmount = payment.Amount

	tx := database.DB.Begin()

	if err := tx.Create(&payment).Error; err != nil {
		tx.Rollback()
		utils.InternalServerError(c, "创建缴费记录失败")
		return
	}

	if payment.Type == "tuition" && payment.CourseID != nil {
		courseID := *payment.CourseID
		var course models.Course
		if err := tx.First(&course, courseID).Error; err == nil {
			studentCourse := models.StudentCourse{
				StudentID:  payment.StudentID,
				CourseID:   courseID,
				TotalHours: course.TotalHours,
				UsedHours:  0,
			}
			tx.Where(models.StudentCourse{StudentID: payment.StudentID, CourseID: courseID}).
				FirstOrCreate(&studentCourse)
		}
	}

	tx.Commit()
	utils.Success(c, payment)
}

func UpdatePayment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}

	// 退费计数器与净收入只能通过退费流程变更，禁止直接改写
	for _, field := range []string{"refunded_amount", "pending_refund_amount", "net_income", "available_refund_amount"} {
		delete(updates, field)
	}

	// 修改实收金额时，必须在锁内校验不得小于已占退款额度，并同步净收入
	if newAmount, ok := updates["amount"]; ok {
		amount, err := strconv.ParseFloat(fmt.Sprintf("%v", newAmount), 64)
		if err != nil || amount < 0 {
			utils.BadRequest(c, "金额格式错误")
			return
		}

		var occupied float64
		err = database.DB.Transaction(func(tx *gorm.DB) error {
			var payment models.Payment
			if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&payment, id).Error; e != nil {
				return e
			}
			occupied = payment.RefundedAmount + payment.PendingRefundAmount
			if amount+refundEpsilon < occupied {
				return errRefundExceedPaid
			}
			updates["net_income"] = amount - payment.RefundedAmount
			return tx.Model(&models.Payment{}).Where("id = ?", id).Updates(updates).Error
		})

		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				utils.NotFound(c, "缴费记录不存在")
			} else if errors.Is(err, errRefundExceedPaid) {
				utils.BadRequest(c, "实收金额不得小于已退与待审金额合计")
			} else {
				utils.InternalServerError(c, "更新失败")
			}
			return
		}

		var payment models.Payment
		database.DB.First(&payment, id)
		utils.Success(c, payment)
		return
	}

	var count int64
	if e := database.DB.Model(&models.Payment{}).Where("id = ?", id).Count(&count).Error; e != nil {
		utils.InternalServerError(c, "更新失败")
		return
	}
	if count == 0 {
		utils.NotFound(c, "缴费记录不存在")
		return
	}

	if err := database.DB.Model(&models.Payment{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		utils.InternalServerError(c, "更新失败")
		return
	}

	var payment models.Payment
	database.DB.First(&payment, id)
	utils.Success(c, payment)
}

func DeletePayment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

	var refundCount int64
	database.DB.Model(&models.Refund{}).Where("payment_id = ?", id).Count(&refundCount)
	if refundCount > 0 {
		utils.BadRequest(c, "该缴费存在退费申请，不能删除，请先处理退费记录")
		return
	}

	if err := database.DB.Delete(&models.Payment{}, id).Error; err != nil {
		utils.InternalServerError(c, "删除失败")
		return
	}

	utils.Success(c, nil)
}

func CreateRefund(c *gin.Context) {
	var req struct {
		PaymentID uint    `json:"payment_id" binding:"required"`
		StudentID uint    `json:"student_id"`
		Amount    float64 `json:"amount" binding:"required"`
		Reason    string  `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}

	if req.Amount <= 0 {
		utils.BadRequest(c, errRefundAmountInvalid.Error())
		return
	}

	var refundID uint
	err := database.DB.Transaction(func(tx *gorm.DB) error {
		// 锁定对应缴费单，串行化同一笔缴费的退费申请，避免并发超额
		var payment models.Payment
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&payment, req.PaymentID).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return errRefundPaymentGone
			}
			return e
		}

		if payment.Status != "paid" {
			return errRefundPaymentGone
		}

		studentID := req.StudentID
		if studentID == 0 {
			studentID = payment.StudentID
		} else if studentID != payment.StudentID {
			return errRefundStudentMismatch
		}

		// 待审 + 已退 + 本次申请 不得超过实收；超出整单拒绝，不改任何金额
		if payment.RefundedAmount+payment.PendingRefundAmount+req.Amount > payment.Amount+refundEpsilon {
			return errRefundExceedPaid
		}

		newPending := payment.PendingRefundAmount + req.Amount
		if e := tx.Model(&models.Payment{}).Where("id = ?", payment.ID).
			Update("pending_refund_amount", newPending).Error; e != nil {
			return e
		}

		refund := models.Refund{
			StudentID: studentID,
			PaymentID: payment.ID,
			Amount:    req.Amount,
			Reason:    req.Reason,
			Status:    "pending",
		}
		if e := tx.Create(&refund).Error; e != nil {
			return e
		}
		refundID = refund.ID
		return nil
	})

	if err != nil {
		switch {
		case errors.Is(err, errRefundPaymentGone), errors.Is(err, errRefundStudentMismatch):
			utils.BadRequest(c, err.Error())
		case errors.Is(err, errRefundExceedPaid):
			utils.BadRequest(c, err.Error())
		default:
			utils.InternalServerError(c, "创建退费申请失败")
		}
		return
	}

	var refund models.Refund
	if err := database.DB.Preload("Payment").Preload("Student").First(&refund, refundID).Error; err != nil {
		utils.InternalServerError(c, "退费申请已创建但回读失败")
		return
	}
	utils.Success(c, refund)
}

func GetRefunds(c *gin.Context) {
	query := database.DB.Model(&models.Refund{}).
		Preload("Payment").Preload("Payment.Course").Preload("Student")

	if status := c.Query("status"); status != "" {
		query = query.Where("refunds.status = ?", status)
	}
	if paymentID := c.Query("payment_id"); paymentID != "" {
		query = query.Where("payment_id = ?", paymentID)
	}
	if studentID := c.Query("student_id"); studentID != "" {
		query = query.Where("student_id = ?", studentID)
	}

	var refunds []models.Refund
	if err := query.Order("refunds.created_at DESC").Find(&refunds).Error; err != nil {
		utils.InternalServerError(c, "查询失败")
		return
	}

	utils.Success(c, refunds)
}

// ProcessRefund 批准或驳回复核退费申请。
// 只有待审核申请可处理；重复/并发请求依靠行锁 + 条件更新保证只有一次成功，
// 任何失败路径都在事务内回滚，金额不发生变化。
func ProcessRefund(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	userID, _ := c.Get("user_id")

	var req struct {
		Status       string `json:"status" binding:"required"`
		RejectReason string `json:"reject_reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}
	if req.Status != "approved" && req.Status != "rejected" {
		utils.BadRequest(c, errRefundActionInvalid.Error())
		return
	}

	processedBy := userID.(uint)

	err := database.DB.Transaction(func(tx *gorm.DB) error {
		// 先锁缴费单再锁退费单，固定加锁顺序避免并发死锁
		var refund models.Refund
		if e := tx.First(&refund, id).Error; e != nil {
			if errors.Is(e, gorm.ErrRecordNotFound) {
				return gorm.ErrRecordNotFound
			}
			return e
		}

		var payment models.Payment
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&payment, refund.PaymentID).Error; e != nil {
			return e
		}
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&refund, id).Error; e != nil {
			return e
		}

		if refund.Status != "pending" {
			return errRefundNotPending
		}

		today := time.Now().Format("2006-01-02")
		refundUpdates := map[string]interface{}{
			"status":       req.Status,
			"processed_by": processedBy,
		}

		paymentUpdates := map[string]interface{}{}

		if req.Status == "approved" {
			// 批准前再次核对：已退 + 本次不得超过（实收 - 其他待审）
			maxRefundable := payment.Amount - (payment.PendingRefundAmount - refund.Amount)
			if payment.RefundedAmount+refund.Amount > maxRefundable+refundEpsilon {
				return errRefundExceedPaid
			}
			refundUpdates["refund_date"] = today
			refundUpdates["reject_reason"] = ""
			paymentUpdates["refunded_amount"] = payment.RefundedAmount + refund.Amount
			paymentUpdates["pending_refund_amount"] = payment.PendingRefundAmount - refund.Amount
			paymentUpdates["net_income"] = payment.Amount - payment.RefundedAmount - refund.Amount
		} else {
			refundUpdates["reject_reason"] = req.RejectReason
			paymentUpdates["pending_refund_amount"] = payment.PendingRefundAmount - refund.Amount
		}

		// 条件更新：只有仍是待审核的申请才能落库，重复/并发最多一行生效
		result := tx.Model(&models.Refund{}).
			Where("id = ? AND status = ?", id, "pending").
			Updates(refundUpdates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errRefundNotPending
		}

		if e := tx.Model(&models.Payment{}).Where("id = ?", payment.ID).
			Updates(paymentUpdates).Error; e != nil {
			return e
		}
		return nil
	})

	if err != nil {
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			utils.NotFound(c, "退费申请不存在")
		case errors.Is(err, errRefundNotPending):
			utils.BadRequest(c, "该申请已处理，请勿重复操作")
		case errors.Is(err, errRefundExceedPaid):
			utils.BadRequest(c, errRefundExceedPaid.Error())
		default:
			utils.InternalServerError(c, "处理退费失败")
		}
		return
	}

	var refund models.Refund
	if err := database.DB.Preload("Payment").Preload("Payment.Course").Preload("Student").
		First(&refund, id).Error; err != nil {
		utils.InternalServerError(c, "退费处理成功但回读失败")
		return
	}
	utils.Success(c, refund)
}

func GetFinanceReports(c *gin.Context) {
	reportType := c.DefaultQuery("type", "daily")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	var results []struct {
		Period        string  `json:"period"`
		PaymentMethod string  `json:"payment_method"`
		PaymentCount  int64   `json:"payment_count"`
		TotalIncome   float64 `json:"total_income"`
		RefundAmount  float64 `json:"refund_amount"`
		NetIncome     float64 `json:"net_income"`
	}
	var groupBy string

	switch reportType {
	case "daily":
		groupBy = "DATE(payment_date)"
	case "monthly":
		groupBy = "SUBSTRING(payment_date, 1, 7)"
	case "yearly":
		groupBy = "SUBSTRING(payment_date, 1, 4)"
	default:
		groupBy = "DATE(payment_date)"
	}

	// 保持按收款方式分组的原统计口径，同时补充退费金额与财务净收入
	query := database.DB.Model(&models.Payment{}).
		Select(fmt.Sprintf(`%s as period,
			SUM(amount) as total_income,
			SUM(amount - net_income) as refund_amount,
			SUM(net_income) as net_income,
			COUNT(*) as payment_count,
			payment_method`, groupBy)).
		Where("status = ?", "paid")

	if startDate != "" {
		query = query.Where("payment_date >= ?", startDate)
	}
	if endDate != "" {
		query = query.Where("payment_date <= ?", endDate)
	}

	if err := query.Group(groupBy + ", payment_method").Order("period DESC").Find(&results).Error; err != nil {
		utils.InternalServerError(c, "查询失败")
		return
	}

	utils.Success(c, results)
}

func generateReceiptNo() string {
	return fmt.Sprintf("R%s%06d", time.Now().Format("20060102150405"), time.Now().UnixNano()%1000000)
}
