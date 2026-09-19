package controllers

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"edu-train/database"
	"edu-train/models"
	"edu-train/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 退费单状态：pending 待审 / approved 已退 / rejected 已驳回
var refundableStatuses = []string{"pending", "approved"}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

// fillRefundDerived 计算每笔缴费的待审退费、可退金额与净收入（数据来自数据库，刷新后保持一致）
func fillRefundDerived(payments []models.Payment) {
	if len(payments) == 0 {
		return
	}

	ids := make([]uint, 0, len(payments))
	for _, p := range payments {
		ids = append(ids, p.ID)
	}

	type refundAgg struct {
		PaymentID uint
		Status    string
		Total     float64
	}
	var aggs []refundAgg
	database.DB.Model(&models.Refund{}).
		Select("payment_id, status, SUM(amount) AS total").
		Where("payment_id IN ? AND status IN ?", ids, refundableStatuses).
		Group("payment_id, status").
		Scan(&aggs)

	pendingMap := map[uint]float64{}
	for _, a := range aggs {
		if a.Status == "pending" {
			pendingMap[a.PaymentID] += a.Total
		}
	}

	for i := range payments {
		p := &payments[i]
		p.RefundedAmount = round2(p.RefundedAmount)
		p.PendingRefundAmount = round2(pendingMap[p.ID])
		refundable := p.Amount - p.RefundedAmount - p.PendingRefundAmount
		if refundable < 0 {
			refundable = 0
		}
		p.RefundableAmount = round2(refundable)
		p.NetIncome = round2(p.Amount - p.RefundedAmount)
	}
}

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

	fillRefundDerived(payments)

	utils.Success(c, gin.H{
		"list":  payments,
		"total": total,
		"page":  page,
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

	list := []models.Payment{payment}
	fillRefundDerived(list)
	payment = list[0]

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

	var payment models.Payment
	if err := database.DB.First(&payment, id).Error; err != nil {
		utils.NotFound(c, "缴费记录不存在")
		return
	}

	var updates map[string]interface{}
	if err := c.ShouldBindJSON(&updates); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}

	// 退费相关金额只能由退费流程维护，禁止直接修改
	for _, key := range []string{"refunded_amount", "pending_refund_amount", "refundable_amount", "net_income"} {
		delete(updates, key)
	}

	if err := database.DB.Model(&payment).Updates(updates).Error; err != nil {
		utils.InternalServerError(c, "更新失败")
		return
	}

	utils.Success(c, payment)
}

func DeletePayment(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))

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

	amount := round2(req.Amount)
	if amount <= 0 {
		utils.BadRequest(c, "退费金额必须大于0")
		return
	}

	tx := database.DB.Begin()
	if tx.Error != nil {
		utils.InternalServerError(c, "创建退费申请失败")
		return
	}

	// 锁定缴费单，串行化同一笔缴费的并发退费申请（仅 MySQL 支持行锁）
	var payment models.Payment
	paymentQuery := tx
	if tx.Dialector.Name() == "mysql" {
		paymentQuery = paymentQuery.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := paymentQuery.First(&payment, req.PaymentID).Error; err != nil {
		tx.Rollback()
		utils.NotFound(c, "缴费记录不存在")
		return
	}

	// 待审与已退金额合计不得超过实收
	var used float64
	if err := tx.Model(&models.Refund{}).
		Where("payment_id = ? AND status IN ?", payment.ID, refundableStatuses).
		Select("COALESCE(SUM(amount), 0)").
		Row().Scan(&used); err != nil {
		tx.Rollback()
		utils.InternalServerError(c, "创建退费申请失败")
		return
	}

	if used+amount > payment.Amount+1e-9 {
		tx.Rollback()
		utils.BadRequest(c, fmt.Sprintf("退费金额超出可退金额 %.2f 元，本次申请已整单拒绝", round2(payment.Amount-used)))
		return
	}

	studentID := req.StudentID
	if studentID == 0 {
		studentID = payment.StudentID
	}

	refund := models.Refund{
		PaymentID: payment.ID,
		StudentID: studentID,
		Amount:    amount,
		Reason:    req.Reason,
		Status:    "pending",
	}
	if err := tx.Create(&refund).Error; err != nil {
		tx.Rollback()
		utils.InternalServerError(c, "创建退费申请失败")
		return
	}

	if err := tx.Commit().Error; err != nil {
		utils.InternalServerError(c, "创建退费申请失败")
		return
	}

	utils.Success(c, refund)
}

func GetRefunds(c *gin.Context) {
	paymentID := c.Query("payment_id")
	status := c.Query("status")

	query := database.DB.Model(&models.Refund{}).Preload("Payment.Student")
	if paymentID != "" {
		query = query.Where("payment_id = ?", paymentID)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}

	var refunds []models.Refund
	if err := query.Order("created_at DESC").Find(&refunds).Error; err != nil {
		utils.InternalServerError(c, "查询失败")
		return
	}

	utils.Success(c, refunds)
}

func ProcessRefund(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	userID, _ := c.Get("user_id")

	var req struct {
		Status string `json:"status" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		utils.BadRequest(c, "参数错误")
		return
	}

	if req.Status != "approved" && req.Status != "rejected" {
		utils.BadRequest(c, "无效的处理状态，仅支持 approved（批准）或 rejected（驳回）")
		return
	}

	tx := database.DB.Begin()
	if tx.Error != nil {
		utils.InternalServerError(c, "处理退费失败")
		return
	}

	var refund models.Refund
	if err := tx.First(&refund, id).Error; err != nil {
		tx.Rollback()
		utils.NotFound(c, "退费申请不存在")
		return
	}

	processedBy := userID.(uint)
	updates := map[string]interface{}{
		"status":       req.Status,
		"processed_by": processedBy,
	}
	if req.Status == "approved" {
		updates["refund_date"] = time.Now().Format("2006-01-02")
	}

	// 只有待审申请可被处理；条件更新保证重复或并发处理只成功一次
	result := tx.Model(&models.Refund{}).
		Where("id = ? AND status = ?", refund.ID, "pending").
		Updates(updates)
	if result.Error != nil {
		tx.Rollback()
		utils.InternalServerError(c, "处理退费失败")
		return
	}
	if result.RowsAffected == 0 {
		tx.Rollback()
		utils.BadRequest(c, "该退费申请已处理，不能重复操作")
		return
	}

	if req.Status == "approved" {
		// 批准后更新已退金额（可退金额与净收入随之变化），并保证已退合计不超过实收
		payResult := tx.Model(&models.Payment{}).
			Where("id = ? AND refunded_amount + ? <= amount", refund.PaymentID, refund.Amount).
			Update("refunded_amount", gorm.Expr("refunded_amount + ?", refund.Amount))
		if payResult.Error != nil {
			tx.Rollback()
			utils.InternalServerError(c, "处理退费失败")
			return
		}
		if payResult.RowsAffected == 0 {
			tx.Rollback()
			utils.BadRequest(c, "可退金额不足，退费失败")
			return
		}
	}

	if err := tx.Commit().Error; err != nil {
		utils.InternalServerError(c, "处理退费失败")
		return
	}

	database.DB.Preload("Payment.Student").First(&refund, refund.ID)
	utils.Success(c, refund)
}

func GetFinanceReports(c *gin.Context) {
	reportType := c.DefaultQuery("type", "daily")
	startDate := c.Query("start_date")
	endDate := c.Query("end_date")

	var results []map[string]interface{}
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

	query := database.DB.Model(&models.Payment{}).
		Select(fmt.Sprintf("%s as period, SUM(amount) as total_income, COUNT(*) as payment_count, payment_method", groupBy)).
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

	// 已批准退费按缴费期间与收款方式归集，用于计算净收入；原收款方式统计字段保持不变
	refundQuery := database.DB.Model(&models.Refund{}).
		Select(fmt.Sprintf("%s as period, payments.payment_method, SUM(refunds.amount) as total_refund", groupBy)).
		Joins("JOIN payments ON payments.id = refunds.payment_id").
		Where("refunds.status = ?", "approved")
	if startDate != "" {
		refundQuery = refundQuery.Where("payments.payment_date >= ?", startDate)
	}
	if endDate != "" {
		refundQuery = refundQuery.Where("payments.payment_date <= ?", endDate)
	}

	var refundRows []map[string]interface{}
	if err := refundQuery.Group(groupBy + ", payments.payment_method").Find(&refundRows).Error; err != nil {
		utils.InternalServerError(c, "查询失败")
		return
	}

	refundMap := map[string]float64{}
	for _, r := range refundRows {
		refundMap[reportKey(r["period"], r["payment_method"])] += toFloat(r["total_refund"])
	}

	for _, row := range results {
		key := reportKey(row["period"], row["payment_method"])
		refunded := round2(refundMap[key])
		row["total_refund"] = refunded
		row["net_income"] = round2(toFloat(row["total_income"]) - refunded)
		delete(refundMap, key)
	}

	// 退费所属期间没有缴费记录时补充一行，保证净收入完整
	for key, refunded := range refundMap {
		parts := strings.SplitN(key, "|", 2)
		results = append(results, map[string]interface{}{
			"period":         parts[0],
			"payment_method": parts[1],
			"total_income":   0.0,
			"payment_count":  0,
			"total_refund":   round2(refunded),
			"net_income":     round2(-refunded),
		})
	}

	utils.Success(c, results)
}

func reportKey(period, method interface{}) string {
	return toString(period) + "|" + toString(method)
}

func toString(v interface{}) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case time.Time:
		return t.Format("2006-01-02")
	default:
		return fmt.Sprintf("%v", v)
	}
}

func toFloat(v interface{}) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case float32:
		return float64(t)
	case int64:
		return float64(t)
	case []byte:
		f, _ := strconv.ParseFloat(string(t), 64)
		return f
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	}
	return 0
}

func generateReceiptNo() string {
	return fmt.Sprintf("R%s%06d", time.Now().Format("20060102150405"), time.Now().UnixNano()%1000000)
}
