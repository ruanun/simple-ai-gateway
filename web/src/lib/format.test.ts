import { describe, expect, it } from 'vitest'
import { errMsg, microsToUsd, num, usd, usdToMicros } from './format'

describe('format', () => {
  it('usd 按 4 位小数显示 micros', () => {
    expect(usd(12345)).toBe('$0.0123')
    expect(usd(0)).toBe('$0.0000')
  })
  it('usdToMicros 收敛浮点误差并对非法输入回退 0', () => {
    expect(usdToMicros('1.2345678')).toBe(1234568)
    expect(usdToMicros('abc')).toBe(0)
    expect(usdToMicros(Number.NaN)).toBe(0)
  })
  it('microsToUsd 与 num 基础换算', () => {
    expect(microsToUsd(2_000_000)).toBe(2)
    expect(num(1234567)).toBe('1,234,567')
  })
  it('errMsg 提取 Error.message', () => {
    expect(errMsg(new Error('boom'))).toBe('boom')
    expect(errMsg('x')).toBe('操作失败')
  })
})
