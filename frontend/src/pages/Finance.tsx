import { useEffect, useState } from 'react'
import {
  Table,
  Card,
  Button,
  Modal,
  Form,
  Space,
  Popconfirm,
  message,
  Typography,
  Select,
  DatePicker,
  InputNumber,
  Tabs,
  Radio,
  Tag,
  Statistic,
  Row,
  Col,
  Input,
} from 'antd'
import { PlusOutlined, EditOutlined, DeleteOutlined, TransactionOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { paymentApi, refundApi, studentApi, courseApi } from '@/services/api'

const { Title, Text } = Typography
const { Option } = Select
const { TextArea } = Input

const paymentMethods = [
  { value: 'cash', label: '现金' },
  { value: 'wechat', label: '微信' },
  { value: 'alipay', label: '支付宝' },
  { value: 'bank', label: '银行转账' },
]

const paymentTypes = [
  { value: 'tuition', label: '学费' },
  { value: 'deposit', label: '定金' },
  { value: 'other', label: '其他' },
]

const refundStatusMap: Record<string, { label: string; color: string }> = {
  pending: { label: '待审核', color: 'gold' },
  approved: { label: '已退费', color: 'green' },
  rejected: { label: '已驳回', color: 'red' },
}

const money = (v?: number | string) => {
  const n = Number(v ?? 0)
  return Number.isFinite(n) ? n.toFixed(2) : '0.00'
}

function Finance() {
  const [paymentLoading, setPaymentLoading] = useState(false)
  const [refundLoading, setRefundLoading] = useState(false)
  const [payments, setPayments] = useState<any[]>([])
  const [refunds, setRefunds] = useState<any[]>([])
  const [reports, setReports] = useState<any[]>([])
  const [reportType, setReportType] = useState('monthly')
  const [students, setStudents] = useState<any[]>([])
  const [courses, setCourses] = useState<any[]>([])

  const [paymentModalVisible, setPaymentModalVisible] = useState(false)
  const [paymentModalType, setPaymentModalType] = useState<'create' | 'edit'>('create')
  const [selectedPayment, setSelectedPayment] = useState<any>(null)
  const [paymentForm] = Form.useForm()

  const [refundModalVisible, setRefundModalVisible] = useState(false)
  const [refundTarget, setRefundTarget] = useState<any>(null)
  const [refundSubmitting, setRefundSubmitting] = useState(false)
  const [refundForm] = Form.useForm()

  const [rejectModalVisible, setRejectModalVisible] = useState(false)
  const [rejectTarget, setRejectTarget] = useState<any>(null)
  const [rejectReason, setRejectReason] = useState('')

  const fetchPayments = async () => {
    try {
      setPaymentLoading(true)
      const res: any = await paymentApi.list({ page: 1, page_size: 1000 })
      setPayments(res.list || [])
    } catch (error) {
      console.error('Fetch payments error:', error)
    } finally {
      setPaymentLoading(false)
    }
  }

  const fetchRefunds = async () => {
    try {
      setRefundLoading(true)
      const res: any = await refundApi.list()
      setRefunds(res || [])
    } catch (error) {
      console.error('Fetch refunds error:', error)
    } finally {
      setRefundLoading(false)
    }
  }

  const fetchReports = async (type = reportType) => {
    try {
      const res: any = await paymentApi.reports({ type })
      setReports(res || [])
    } catch (error) {
      console.error('Fetch reports error:', error)
    }
  }

  const fetchOptions = async () => {
    try {
      const [studentsRes, coursesRes] = await Promise.all([
        studentApi.list({ page_size: 1000 }),
        courseApi.list(),
      ])
      setStudents((studentsRes as any)?.list || [])
      setCourses((coursesRes as any)?.list || [])
    } catch (error) {
      console.error('Fetch options error:', error)
    }
  }

  useEffect(() => {
    fetchPayments()
    fetchRefunds()
    fetchReports('monthly')
    fetchOptions()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const refreshAll = () => {
    fetchPayments()
    fetchRefunds()
    fetchReports()
  }

  // ---------- 缴费 ----------

  const handleCreatePayment = () => {
    setPaymentModalType('create')
    setSelectedPayment(null)
    paymentForm.resetFields()
    paymentForm.setFieldsValue({
      payment_date: dayjs(),
      payment_method: 'wechat',
      type: 'tuition',
    })
    setPaymentModalVisible(true)
  }

  const handleEditPayment = (payment: any) => {
    setPaymentModalType('edit')
    setSelectedPayment(payment)
    paymentForm.setFieldsValue({
      ...payment,
      payment_date: payment.payment_date ? dayjs(payment.payment_date) : undefined,
    })
    setPaymentModalVisible(true)
  }

  const handleDeletePayment = async (id: number) => {
    try {
      await paymentApi.delete(id)
      message.success('删除成功')
      refreshAll()
    } catch (error) {
      console.error('Delete payment error:', error)
    }
  }

  const handlePaymentSubmit = async () => {
    try {
      const values = await paymentForm.validateFields()
      const data = {
        ...values,
        payment_date: values.payment_date.format('YYYY-MM-DD'),
      }

      if (paymentModalType === 'create') {
        await paymentApi.create(data)
        message.success('创建成功')
      } else if (selectedPayment?.id) {
        await paymentApi.update(selectedPayment.id, data)
        message.success('更新成功')
      }
      setPaymentModalVisible(false)
      refreshAll()
    } catch (error) {
      console.error('Payment submit error:', error)
    }
  }

  // ---------- 退费 ----------

  const handleOpenRefund = (payment: any) => {
    setRefundTarget(payment)
    refundForm.resetFields()
    refundForm.setFieldsValue({ amount: payment.available_refund_amount })
    setRefundModalVisible(true)
  }

  const handleRefundSubmit = async () => {
    try {
      const values = await refundForm.validateFields()
      setRefundSubmitting(true)
      await refundApi.create({
        payment_id: refundTarget.id,
        student_id: refundTarget.student_id,
        amount: values.amount,
        reason: values.reason,
      })
      message.success('退费申请已提交，等待审核')
      setRefundModalVisible(false)
      refreshAll()
    } catch (error) {
      console.error('Create refund error:', error)
    } finally {
      setRefundSubmitting(false)
    }
  }

  const handleApprove = async (refund: any) => {
    try {
      await refundApi.process(refund.id, { status: 'approved' })
      message.success('已批准退费')
      refreshAll()
    } catch (error) {
      console.error('Approve refund error:', error)
    }
  }

  const handleOpenReject = (refund: any) => {
    setRejectTarget(refund)
    setRejectReason('')
    setRejectModalVisible(true)
  }

  const handleRejectSubmit = async () => {
    try {
      await refundApi.process(rejectTarget.id, {
        status: 'rejected',
        reject_reason: rejectReason,
      })
      message.success('已驳回申请')
      setRejectModalVisible(false)
      refreshAll()
    } catch (error) {
      console.error('Reject refund error:', error)
    }
  }

  // ---------- 表格列 ----------

  const paymentColumns = [
    {
      title: '学员',
      dataIndex: ['student', 'name'],
      key: 'student',
      render: (name: string) => name || '-',
    },
    {
      title: '课程',
      dataIndex: ['course', 'name'],
      key: 'course',
      render: (name: string) => name || '-',
    },
    {
      title: '实收金额(元)',
      dataIndex: 'amount',
      key: 'amount',
      render: (v: number) => <Text strong>{money(v)}</Text>,
    },
    {
      title: '已退(元)',
      dataIndex: 'refunded_amount',
      key: 'refunded_amount',
      render: (v: number) => money(v),
    },
    {
      title: '待审(元)',
      dataIndex: 'pending_refund_amount',
      key: 'pending_refund_amount',
      render: (v: number) => (v > 0 ? <Text type="warning">{money(v)}</Text> : money(v)),
    },
    {
      title: '可退金额(元)',
      dataIndex: 'available_refund_amount',
      key: 'available_refund_amount',
      render: (v: number) => (
        <Text type={v > 0.005 ? 'success' : 'secondary'}>{money(v)}</Text>
      ),
    },
    {
      title: '净收入(元)',
      dataIndex: 'net_income',
      key: 'net_income',
      render: (v: number) => <Text strong>{money(v)}</Text>,
    },
    {
      title: '支付方式',
      dataIndex: 'payment_method',
      key: 'payment_method',
      render: (method: string) => paymentMethods.find((o) => o.value === method)?.label || method,
    },
    {
      title: '类型',
      dataIndex: 'type',
      key: 'type',
      render: (type: string) => paymentTypes.find((o) => o.value === type)?.label || type,
    },
    {
      title: '日期',
      dataIndex: 'payment_date',
      key: 'payment_date',
    },
    {
      title: '收据号',
      dataIndex: 'receipt_no',
      key: 'receipt_no',
    },
    {
      title: '操作',
      key: 'action',
      render: (_: any, record: any) => (
        <Space size="small" wrap>
          <Button type="link" size="small" onClick={() => handleEditPayment(record)}>
            <EditOutlined /> 编辑
          </Button>
          <Button
            type="link"
            size="small"
            disabled={Number(record.available_refund_amount ?? 0) <= 0.005}
            onClick={() => handleOpenRefund(record)}
          >
            <TransactionOutlined /> 申请退费
          </Button>
          <Popconfirm
            title="确定删除?"
            onConfirm={() => handleDeletePayment(record.id!)}
            okText="确定"
            cancelText="取消"
          >
            <Button type="link" size="small" danger>
              <DeleteOutlined /> 删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const refundColumns = [
    {
      title: '申请时间',
      dataIndex: 'created_at',
      key: 'created_at',
      render: (v: string) => (v ? dayjs(v).format('YYYY-MM-DD HH:mm') : '-'),
    },
    {
      title: '学员',
      key: 'student',
      render: (_: any, record: any) => record.student?.name || record.payment?.student?.name || '-',
    },
    {
      title: '关联收据',
      key: 'receipt',
      render: (_: any, record: any) => record.payment?.receipt_no || `#${record.payment_id}`,
    },
    {
      title: '课程',
      key: 'course',
      render: (_: any, record: any) => record.payment?.course?.name || '-',
    },
    {
      title: '申请金额(元)',
      dataIndex: 'amount',
      key: 'amount',
      render: (v: number) => money(v),
    },
    {
      title: '状态',
      dataIndex: 'status',
      key: 'status',
      render: (status: string) => {
        const s = refundStatusMap[status] || { label: status, color: 'default' }
        return <Tag color={s.color}>{s.label}</Tag>
      },
    },
    {
      title: '申请原因',
      dataIndex: 'reason',
      key: 'reason',
      render: (v: string) => v || '-',
    },
    {
      title: '驳回原因',
      dataIndex: 'reject_reason',
      key: 'reject_reason',
      render: (v: string) => v || '-',
    },
    {
      title: '退费日期',
      dataIndex: 'refund_date',
      key: 'refund_date',
      render: (v: string) => v || '-',
    },
    {
      title: '操作',
      key: 'action',
      render: (_: any, record: any) =>
        record.status === 'pending' ? (
          <Space size="small">
            <Popconfirm
              title="确认批准该退费？"
              description={`将退回 ${money(record.amount)} 元，并扣减可退金额与净收入`}
              onConfirm={() => handleApprove(record)}
              okText="确认批准"
              cancelText="取消"
            >
              <Button type="link" size="small">
                批准
              </Button>
            </Popconfirm>
            <Button type="link" size="small" danger onClick={() => handleOpenReject(record)}>
              驳回
            </Button>
          </Space>
        ) : (
          <Text type="secondary">已处理</Text>
        ),
    },
  ]

  const reportColumns = [
    { title: '期间', dataIndex: 'period', key: 'period' },
    {
      title: '收款方式',
      dataIndex: 'payment_method',
      key: 'payment_method',
      render: (method: string) => paymentMethods.find((o) => o.value === method)?.label || method,
    },
    { title: '缴费笔数', dataIndex: 'payment_count', key: 'payment_count' },
    {
      title: '实收合计(元)',
      dataIndex: 'total_income',
      key: 'total_income',
      render: (v: any) => money(v),
    },
    {
      title: '退费金额(元)',
      dataIndex: 'refund_amount',
      key: 'refund_amount',
      render: (v: any) => money(v),
    },
    {
      title: '净收入(元)',
      dataIndex: 'net_income',
      key: 'net_income',
      render: (v: any) => <Text strong>{money(v)}</Text>,
    },
  ]

  const sumField = (list: any[], field: string) =>
    list.reduce((acc, item) => acc + Number(item[field] ?? 0), 0)

  // ---------- 渲染 ----------

  return (
    <div>
      <Title level={3} style={{ marginBottom: 24 }}>
        财务管理
      </Title>

      <Tabs
        items={[
          {
            key: 'payments',
            label: '缴费记录',
            children: (
              <Card>
                <Row gutter={24} style={{ marginBottom: 16 }}>
                  <Col span={6}>
                    <Statistic title="实收合计(元)" value={money(sumField(payments, 'amount'))} />
                  </Col>
                  <Col span={6}>
                    <Statistic
                      title="已退合计(元)"
                      valueStyle={{ color: '#cf1322' }}
                      value={money(sumField(payments, 'refunded_amount'))}
                    />
                  </Col>
                  <Col span={6}>
                    <Statistic
                      title="待审退费(元)"
                      valueStyle={{ color: '#d48806' }}
                      value={money(sumField(payments, 'pending_refund_amount'))}
                    />
                  </Col>
                  <Col span={6}>
                    <Statistic title="财务净收入(元)" value={money(sumField(payments, 'net_income'))} />
                  </Col>
                </Row>

                <div style={{ marginBottom: 16, display: 'flex', justifyContent: 'flex-end' }}>
                  <Button type="primary" icon={<PlusOutlined />} onClick={handleCreatePayment}>
                    新增缴费
                  </Button>
                </div>

                <Table
                  columns={paymentColumns}
                  dataSource={payments}
                  rowKey="id"
                  loading={paymentLoading}
                  scroll={{ x: 1400 }}
                />
              </Card>
            ),
          },
          {
            key: 'refunds',
            label: '退费申请',
            children: (
              <Card>
                <Table
                  columns={refundColumns}
                  dataSource={refunds}
                  rowKey="id"
                  loading={refundLoading}
                  scroll={{ x: 1200 }}
                />
              </Card>
            ),
          },
          {
            key: 'reports',
            label: '收款统计',
            children: (
              <Card>
                <Row gutter={24} style={{ marginBottom: 16 }} align="middle">
                  <Col>
                    <Radio.Group
                      value={reportType}
                      onChange={(e) => {
                        setReportType(e.target.value)
                        fetchReports(e.target.value)
                      }}
                      optionType="button"
                      buttonStyle="solid"
                      options={[
                        { value: 'daily', label: '按日' },
                        { value: 'monthly', label: '按月' },
                        { value: 'yearly', label: '按年' }
                      ]}
                    />
                  </Col>
                  <Col>
                    <Button onClick={() => fetchReports()}>刷新</Button>
                  </Col>
                </Row>
                <Row gutter={24} style={{ marginBottom: 16 }}>
                  <Col span={8}>
                    <Statistic title="实收合计(元)" value={money(sumField(reports, 'total_income'))} />
                  </Col>
                  <Col span={8}>
                    <Statistic
                      title="退费金额(元)"
                      valueStyle={{ color: '#cf1322' }}
                      value={money(sumField(reports, 'refund_amount'))}
                    />
                  </Col>
                  <Col span={8}>
                    <Statistic title="净收入(元)" value={money(sumField(reports, 'net_income'))} />
                  </Col>
                </Row>
                <Table columns={reportColumns} dataSource={reports} rowKey={(r) => `${r.period}-${r.payment_method}`} />
              </Card>
            ),
          },
        ]}
      />

      {/* 新增/编辑缴费 */}
      <Modal
        title={paymentModalType === 'create' ? '新增缴费' : '编辑缴费'}
        open={paymentModalVisible}
        onOk={handlePaymentSubmit}
        onCancel={() => setPaymentModalVisible(false)}
        destroyOnClose
      >
        <Form form={paymentForm} layout="vertical">
          <Form.Item
            name="student_id"
            label="学员"
            rules={[{ required: true, message: '请选择学员' }]}
          >
            <Select placeholder="请选择学员">
              {students.map((s) => (
                <Option key={s.id} value={s.id}>
                  {s.name}
                </Option>
              ))}
            </Select>
          </Form.Item>
          <Form.Item name="course_id" label="课程">
            <Select placeholder="请选择课程" allowClear>
              {courses.map((c) => (
                <Option key={c.id} value={c.id}>
                  {c.name}
                </Option>
              ))}
            </Select>
          </Form.Item>
          <Form.Item
            name="amount"
            label="金额(元)"
            rules={[{ required: true, message: '请输入金额' }]}
          >
            <InputNumber style={{ width: '100%' }} min={0} precision={2} placeholder="请输入金额" />
          </Form.Item>
          <Form.Item
            name="payment_method"
            label="支付方式"
            rules={[{ required: true, message: '请选择支付方式' }]}
          >
            <Radio.Group>
              {paymentMethods.map((m) => (
                <Radio key={m.value} value={m.value}>
                  {m.label}
                </Radio>
              ))}
            </Radio.Group>
          </Form.Item>
          <Form.Item
            name="type"
            label="类型"
            rules={[{ required: true, message: '请选择类型' }]}
          >
            <Select placeholder="请选择类型">
              {paymentTypes.map((t) => (
                <Option key={t.value} value={t.value}>
                  {t.label}
                </Option>
              ))}
            </Select>
          </Form.Item>
          <Form.Item
            name="payment_date"
            label="缴费日期"
            rules={[{ required: true, message: '请选择日期' }]}
          >
            <DatePicker style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="remarks" label="备注">
            <TextArea rows={2} placeholder="请输入备注" />
          </Form.Item>
        </Form>
      </Modal>

      {/* 申请退费 */}
      <Modal
        title="申请退费"
        open={refundModalVisible}
        onOk={handleRefundSubmit}
        onCancel={() => setRefundModalVisible(false)}
        confirmLoading={refundSubmitting}
        okText="提交申请"
        cancelText="取消"
        destroyOnClose
      >
        {refundTarget && (
          <div style={{ marginBottom: 16 }}>
            <p style={{ marginBottom: 4 }}>
              收据号：<Text strong>{refundTarget.receipt_no}</Text>
            </p>
            <p style={{ marginBottom: 4 }}>实收金额：{money(refundTarget.amount)} 元</p>
            <p style={{ marginBottom: 4 }}>已退金额：{money(refundTarget.refunded_amount)} 元</p>
            <p style={{ marginBottom: 4 }}>
              待审金额：{money(refundTarget.pending_refund_amount)} 元
            </p>
            <p style={{ marginBottom: 0 }}>
              当前可退：<Text type="success">{money(refundTarget.available_refund_amount)} 元</Text>
            </p>
          </div>
        )}
        <Form form={refundForm} layout="vertical">
          <Form.Item
            name="amount"
            label="退费金额(元)"
            rules={[
              { required: true, message: '请输入退费金额' },
              {
                validator: (_, value) => {
                  const max = Number(refundTarget?.available_refund_amount ?? 0)
                  if (value <= 0) return Promise.reject(new Error('退费金额必须大于0'))
                  if (Number(value) > max + 0.001) {
                    return Promise.reject(new Error(`退费金额不能超过当前可退金额 ${money(max)} 元`))
                  }
                  return Promise.resolve()
                },
              },
            ]}
          >
            <InputNumber
              style={{ width: '100%' }}
              min={0}
              precision={2}
              max={Number(refundTarget?.available_refund_amount ?? 0)}
              placeholder="请输入退费金额"
            />
          </Form.Item>
          <Form.Item name="reason" label="退费原因">
            <TextArea rows={3} placeholder="请输入退费原因" />
          </Form.Item>
        </Form>
      </Modal>

      {/* 驳回复核 */}
      <Modal
        title="驳回退费申请"
        open={rejectModalVisible}
        onOk={handleRejectSubmit}
        onCancel={() => setRejectModalVisible(false)}
        okText="确认驳回"
        cancelText="取消"
        okButtonProps={{ danger: true }}
        destroyOnClose
      >
        <p>
          确认驳回金额为 <Text strong>{money(rejectTarget?.amount)}</Text> 元的退费申请？
          驳回后该笔金额将恢复为可退金额，不改变净收入。
        </p>
        <TextArea
          rows={3}
          value={rejectReason}
          onChange={(e) => setRejectReason(e.target.value)}
          placeholder="请输入驳回原因（可选）"
        />
      </Modal>
    </div>
  )
}

export default Finance
