import { afterAll, beforeAll, describe, expect, it } from 'vitest'

import { setLocale } from '@/locales'
import {
  batchRefusalMessage, batchRunActions, formatRunDuration, refusalCodeOf, runOutcomeLabel,
  runStatusLabel, taskOriginLabel, taskSourceLabel, taskStatusFilterOptions, taskStatusLabel,
  taskStatusVariant,
} from './tasks'

/**
 * The task vocabulary (plan 153): one status table, one source table, one
 * duration format, and the batch refusals said in the reader's own language
 * instead of the engine's English sentence.
 */
describe('the batch task vocabulary', () => {
  beforeAll(() => setLocale('zh'))
  afterAll(() => setLocale('en'))

  it('names a status once for tasks and once for the runs under them', () => {
    expect(taskStatusLabel('running')).toBe('运行中')
    expect(taskStatusLabel('completed')).toBe('已完成')
    expect(taskStatusLabel('failed')).toBe('失败')
    expect(taskStatusLabel('stopped')).toBe('已停止')
    expect(taskStatusLabel('')).toBe('等待开始')

    expect(runStatusLabel('completed')).toBe('完成')
    expect(runStatusLabel('running')).toBe('运行中')

    expect(taskStatusVariant('completed')).toBe('success')
    expect(taskStatusVariant('failed')).toBe('error')
    expect(taskStatusVariant('running')).toBe('default')
    expect(taskStatusVariant('stopped')).toBe('gray')
  })

  it('offers 全部状态 first, then the four states the prototype filters by', () => {
    expect(taskStatusFilterOptions().map((option) => option.value)).toEqual(
      ['', 'running', 'completed', 'failed', 'stopped'],
    )
    expect(taskStatusFilterOptions()[0].label).toBe('全部状态')
  })

  it('says which door a task came in by, and what the door recorded', () => {
    expect(taskSourceLabel('api', 'ibs-audit')).toBe('业务系统 · ibs-audit')
    expect(taskSourceLabel('cron', '每个工作日 9:00')).toBe('定时任务 · 每个工作日 9:00')
    expect(taskSourceLabel('web')).toBe('网页 · 手工新建')
    expect(taskSourceLabel('channel')).toBe('通道')
    expect(taskOriginLabel('channel')).toBe('通道')
  })

  it('writes a run\'s duration the way a history row reads it', () => {
    expect(formatRunDuration(100, 100 + 12 * 60 + 8)).toBe('12m 08s')
    expect(formatRunDuration(100, 100 + 42)).toBe('42s')
    expect(formatRunDuration(100, 100 + 120)).toBe('2m')
    expect(formatRunDuration(100, 100 + 3 * 3600 + 5 * 60)).toBe('3h 05m')
    // A run with no end yet has nothing to measure.
    expect(formatRunDuration(100, 0)).toBe('—')
  })

  it('says what a finished run ended on when it has no error text', () => {
    expect(runOutcomeLabel('completed')).toBe('全部处理完成')
    expect(runOutcomeLabel('stopped')).toBe('等待继续')
    expect(runOutcomeLabel('running')).toBe('执行中')
    expect(runOutcomeLabel('pending')).toBe('尚未开始')
  })

  it('says a refused batch turn in the reader\'s language, by its code', () => {
    // Plan 149's codes are the protocol: a caller switches on them, never on
    // the engine's English sentence.
    expect(batchRefusalMessage('batch_execution_running')).toBe('这次执行还在运行，等它结束后再操作。')
    expect(batchRefusalMessage('batch_execution_finished')).toBe('这次执行已经结束，请重新执行。')
    expect(batchRefusalMessage('batch_takes_no_input')).toBe('已停止的执行只能继续，不能再发送内容。')
    expect(batchRefusalMessage('batch_task_input_fixed')).toBe('这条任务已有输入，请重新执行，而不是再发一次内容。')
    expect(batchRefusalMessage('batch_task_input_missing')).toBe('这条任务还没有输入，第一次执行要带上任务内容。')
    expect(batchRefusalMessage('batch_admission_required')).toBe('这次操作没有通过跑批准入检查，请刷新页面再试。')
    expect(batchRefusalMessage('batch_mode_fixed')).toBe('跑批任务不能切换模式。')
    expect(batchRefusalMessage('batch_command_not_allowed')).toBe('跑批任务不能使用内置命令。')
    expect(batchRefusalMessage('batch_session_has_no_task')).toBe('这个会话不是任务执行。')
    // An unknown code keeps the gateway's own words rather than going silent.
    expect(batchRefusalMessage('batch_new_code', 'the engine said this')).toBe('the engine said this')
  })

  it('answers which acts a run in each state allows', () => {
    expect(batchRunActions('running')).toEqual(['stop'])
    expect(batchRunActions('stopped')).toEqual(['continue', 'rerun'])
    expect(batchRunActions('failed')).toEqual(['rerun'])
    expect(batchRunActions('completed')).toEqual([])
    expect(batchRunActions('pending')).toEqual([])
  })

  it('reads the code out of a refusal payload, and nothing out of no payload', () => {
    expect(refusalCodeOf({ code: 'batch_execution_running' })).toBe('batch_execution_running')
    expect(refusalCodeOf(null)).toBe('')
    expect(refusalCodeOf('a sentence')).toBe('')
  })
})
