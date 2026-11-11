import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { Button } from '@/components/ui/button'
import type { TodoList, TodoTask } from '@/types'
import '@/todo.css'

export const todoFormPath = '/api/v1/todos'

export type TodoDraft = { title: string; tags: string; estimate: string }
export type TodoComposer = {
  draft?: TodoDraft & { id: string }
  onChange: (draft: TodoDraft) => void
}

const estimateOptions = [
  [15, '15 min'], [30, '30 min'], [45, '45 min'], [60, '1 hour'], [90, '1 hour 30 min'],
  [120, '2 hours'], [180, '3 hours'], [240, '4 hours'], [360, '6 hours'], [480, '8 hours'],
] as const

const easternDate = new Intl.DateTimeFormat('en-US', {
  timeZone: 'America/New_York', year: 'numeric', month: 'long', day: 'numeric',
})

function useEasternDay() {
  const [day, setDay] = useState(() => easternDate.format(Date.now()))
  useEffect(() => {
    let timer: number
    function updateDay() {
      window.clearTimeout(timer)
      const now = Date.now()
      setDay(easternDate.format(now))
      // Eastern midnight is a whole minute in UTC in both standard and daylight time.
      timer = window.setTimeout(updateDay, 60_000 - now % 60_000)
    }
    updateDay()
    document.addEventListener('visibilitychange', updateDay)
    window.addEventListener('pageshow', updateDay)
    window.addEventListener('focus', updateDay)
    return () => {
      window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', updateDay)
      window.removeEventListener('pageshow', updateDay)
      window.removeEventListener('focus', updateDay)
    }
  }, [])
  return day
}

function Compose({ csrf, composer, pending }: { csrf: string; composer: TodoComposer; pending: boolean }) {
  const { draft, onChange } = composer
  const fields = { title: draft?.title ?? '', tags: draft?.tags ?? '', estimate: draft?.estimate ?? '' }
  const form = useRef<HTMLFormElement>(null)
  const title = useRef<HTMLInputElement>(null)
  const draftWasPresent = useRef(false)
  useLayoutEffect(() => {
    if (draftWasPresent.current && !draft && form.current?.contains(document.activeElement)) {
      title.current?.focus({ preventScroll: true })
    }
    draftWasPresent.current = Boolean(draft)
  }, [draft])
  return (
    <form ref={form} method="post" action={todoFormPath} className="todo-compose">
      <input type="hidden" name="action" value="add" />
      <input type="hidden" name="csrf_token" value={csrf} />
      <input type="hidden" name="task_id" value={draft?.id ?? ''} />
      <div className="todo-compose-main">
        <label>
          <span className="sr-only">New task</span>
          <input ref={title} name="title" placeholder="Add a task…" autoComplete="off" maxLength={180} required
            value={fields.title} onChange={event => onChange({ ...fields, title: event.target.value })} />
        </label>
        <Button type="submit" aria-disabled={pending || !fields.title.trim()}>Add task</Button>
      </div>
      <div className="todo-options">
        <label>
          <span>Tags <small aria-hidden="true">· optional</small></span>
          <input name="tags" placeholder="e.g. Work, Reading" autoComplete="off" maxLength={125}
            aria-describedby="todo-tag-hint" value={fields.tags} onChange={event => onChange({ ...fields, tags: event.target.value })} />
        </label>
        <label>
          <span>Estimate <small aria-hidden="true">· optional</small></span>
          <select name="estimate_minutes" value={fields.estimate} onChange={event => onChange({ ...fields, estimate: event.target.value })}>
            <option value="">No estimate</option>
            {estimateOptions.map(([minutes, label]) => <option key={minutes} value={minutes}>{label}</option>)}
          </select>
        </label>
      </div>
      <p id="todo-tag-hint" className="sr-only">Separate multiple tags with commas.</p>
    </form>
  )
}

function TaskForm({ csrf, task, action, children }: {
  csrf: string; task: TodoTask; action: 'complete' | 'reopen' | 'remove' | 'undo'; children: ReactNode
}) {
  return <form method="post" action={todoFormPath}>
    <input type="hidden" name="csrf_token" value={csrf} />
    <input type="hidden" name="action" value={action} />
    <input type="hidden" name="task_id" value={task.id} />
    <input type="hidden" name="expected_revision" value={task.revision} />
    {children}
  </form>
}

function MoveTask({ task, neighbor, direction, csrf, pending }: {
  task: TodoTask; neighbor?: TodoTask; direction: 'up' | 'down'; csrf: string; pending: boolean
}) {
  const [idempotencyKey] = useState(() => crypto.randomUUID())
  return <form method="post" action={todoFormPath}>
    <input type="hidden" name="action" value="reorder" />
    <input type="hidden" name="csrf_token" value={csrf} />
    <input type="hidden" name="task_id" value={task.id} />
    <input type="hidden" name="expected_revision" value={task.revision} />
    <input type="hidden" name="neighbor_id" value={neighbor?.id ?? ''} />
    <input type="hidden" name="neighbor_revision" value={neighbor?.revision ?? ''} />
    <input type="hidden" name="direction" value={direction} />
    <input type="hidden" name="idempotency_key" value={idempotencyKey} />
    <button type="submit" className="todo-move" data-move={direction}
      aria-label={`Move ${direction}: ${task.title}`} disabled={!neighbor} aria-disabled={pending || undefined}>
      <span aria-hidden="true">{direction === 'up' ? '↑' : '↓'}</span>
    </button>
  </form>
}

function TaskRows({ tasks, csrf, canWrite, pending, reorder = false }: {
  tasks: TodoTask[]; csrf: string; canWrite: boolean; pending: boolean; reorder?: boolean
}) {
  return <ul className="todo-rows" role="list">
    {tasks.map((task, index) => {
      const estimate = estimateOptions.find(([minutes]) => minutes === task.estimate_minutes)
      return <li key={task.id} className="todo-row" data-task-id={task.id}>
        <TaskForm csrf={csrf} task={task} action={task.completed_at ? 'reopen' : 'complete'}>
          <label>
            <input type="checkbox" checked={Boolean(task.completed_at)} disabled={!canWrite} aria-disabled={pending || undefined}
              aria-label={task.title} onChange={event => {
                if (!pending) event.currentTarget.form?.requestSubmit()
              }} />
            <span className="todo-task">
              <span className="todo-title">{task.title}</span>
              {(Boolean(task.tags?.length) || estimate) && <span className="todo-meta">
                {task.tags?.map(tag => <span className="todo-tag" key={tag}>{tag}</span>)}
                {estimate && <span className="todo-estimate"><span className="sr-only">Estimate </span><time dateTime={`PT${estimate[0]}M`}>{estimate[1]}</time></span>}
              </span>}
            </span>
          </label>
        </TaskForm>
        {canWrite && <div className="todo-row-actions">
          {reorder && <div className="todo-moves">
            {(['up', 'down'] as const).map(direction => {
              const neighbor = tasks[index + (direction === 'up' ? -1 : 1)]
              // Keep the retry key until this observed pair changes. A refreshed
              // pair remounts the form so the next move starts a new intent.
              return <MoveTask key={`${direction}:${task.revision}:${neighbor?.id}:${neighbor?.revision}`}
                task={task} neighbor={neighbor} direction={direction} csrf={csrf} pending={pending} />
            })}
          </div>}
          <TaskForm csrf={csrf} task={task} action="remove">
            <button type="submit" className="todo-text-action" aria-label={`Remove ${task.title}`} aria-disabled={pending}>Remove</button>
          </TaskForm>
        </div>}
      </li>
    })}
  </ul>
}

export function Todo({ list, csrf, canWrite, composer, pending, saveNotice }: {
  list: TodoList; csrf: string; canWrite: boolean; composer?: TodoComposer; pending: boolean; saveNotice?: ReactNode
}) {
  const today = useEasternDay()
  const openTasks: TodoTask[] = []
  // Put Today first while keying every group by its date so rows survive midnight.
  const completedByDay = new Map<string, TodoTask[]>([[today, []]])
  for (const task of list.tasks) {
    if (!task.completed_at) {
      openTasks.push(task)
      continue
    }
    const completionDay = easternDate.format(task.completed_at)
    const group = completedByDay.get(completionDay)
    if (group) group.push(task)
    else completedByDay.set(completionDay, [task])
  }
  const completedCount = list.tasks.length - openTasks.length

  const main = useRef<HTMLElement>(null)
  const moveStatus = useRef<HTMLParagraphElement>(null)
  const openHeading = useRef<HTMLHeadingElement>(null)
  const undoButton = useRef<HTMLButtonElement>(null)
  const completedSummary = useRef<HTMLElement>(null)
  const actionFocus = useRef<{
    control: HTMLElement
    fallbacks: (HTMLElement | null | undefined)[]
    removingCompletedTaskID?: string
    undoingCompletedTaskID?: string
    move?: { taskID: string; direction: string; revision: number }
  } | null>(null)
  function rememberFocus(event: FormEvent<HTMLElement>) {
    const form = event.target
    const control = document.activeElement
    if (pending || !(form instanceof HTMLFormElement) || !(control instanceof HTMLElement) || !form.contains(control)) return
    const row = form.closest('li')
    const completedPanel = form.closest('details')
    const action = form.querySelector<HTMLInputElement>('input[name=action]')?.value
    const taskID = form.querySelector<HTMLInputElement>('input[name=task_id]')?.value
    actionFocus.current = {
      control,
      removingCompletedTaskID: completedPanel && action === 'remove' ? taskID : undefined,
      undoingCompletedTaskID: completedCount === 0 && action === 'undo' && list.undo?.completed_at ? taskID : undefined,
      move: action === 'reorder' && taskID ? {
        taskID, direction: form.querySelector<HTMLInputElement>('input[name=direction]')?.value ?? '',
        revision: Number(form.querySelector<HTMLInputElement>('input[name=expected_revision]')?.value),
      } : undefined,
      fallbacks: [
        row?.nextElementSibling?.querySelector<HTMLInputElement>('input[type=checkbox]'),
        row?.previousElementSibling?.querySelector<HTMLInputElement>('input[type=checkbox]'),
        completedPanel?.querySelector('summary'),
        openHeading.current,
      ],
    }
  }
  useLayoutEffect(() => {
    if (pending || !actionFocus.current) return
    const { control, fallbacks, removingCompletedTaskID, undoingCompletedTaskID, move } = actionFocus.current
    actionFocus.current = null
    const orderedOpen = list.tasks.filter(task => !task.completed_at)
    const movedIndex = move ? orderedOpen.findIndex(task => task.id === move.taskID && task.revision > move.revision) : -1
    if (movedIndex >= 0 && moveStatus.current) {
      moveStatus.current.textContent = `${orderedOpen[movedIndex].title} moved to position ${movedIndex + 1} of ${orderedOpen.length}.`
    }
    // Keep any focus the user moved during the save.
    if (control.isConnected || document.activeElement !== document.body) return

    // The refreshed list may include changes from another session.
    let target: HTMLElement | null | undefined
    if (move) {
      const row = [...main.current?.querySelectorAll<HTMLElement>('[data-task-id]') ?? []]
        .find(candidate => candidate.dataset.taskId === move.taskID)
      const button = row?.querySelector<HTMLButtonElement>(`button[data-move="${move.direction}"]`)
      target = button && !button.disabled ? button : row?.querySelector<HTMLInputElement>('input[type=checkbox]')
    } else if (completedCount === 0 && list.undo && list.undo.id === removingCompletedTaskID) {
      target = undoButton.current
    } else if (undoingCompletedTaskID && list.tasks.some(task => task.id === undoingCompletedTaskID && task.completed_at)) {
      target = completedSummary.current
    }
    target ??= fallbacks.find(candidate => candidate?.isConnected)
    target?.focus({ preventScroll: true })
    target?.scrollIntoView({ block: 'nearest' })
  }, [pending, list, completedCount])

  return <main ref={main} id="main" className="todo" tabIndex={-1} onSubmitCapture={rememberFocus}>
    {saveNotice}
    <h1>To-do</h1>
    <p ref={moveStatus} className="sr-only" role="status" aria-live="polite" />
    {canWrite ? composer && <Compose csrf={csrf} composer={composer} pending={pending} />
      : <p className="permission-note">Your sign-in allows you to view tasks. Write permission is required to make changes.</p>}
    <section aria-labelledby="todo-open">
      <div className="todo-list-heading">
        <h2 id="todo-open" ref={openHeading} className="eyebrow" tabIndex={-1}>Open tasks</h2>
        <span>{openTasks.length} {openTasks.length === 1 ? 'task' : 'tasks'}</span>
      </div>
      {openTasks.length ? <TaskRows tasks={openTasks} csrf={csrf} canWrite={canWrite} pending={pending} reorder />
        : <p className="todo-empty">No open tasks.</p>}
    </section>
    {list.undo && canWrite && <div className="todo-undo">
      <span role="status">Task removed.</span>
      <TaskForm csrf={csrf} task={list.undo} action="undo">
        <button ref={undoButton} type="submit" className="todo-text-action" aria-disabled={pending}>Undo</button>
      </TaskForm>
    </div>}
    {completedCount > 0 && (
      <details className="todo-completed">
        <summary ref={completedSummary}>Completed ({completedCount})</summary>
        {[...completedByDay].map(([day, tasks]) => tasks.length > 0 && <div key={day}>
          <h2>{day === today ? 'Today' : day}</h2>
          <TaskRows tasks={tasks} csrf={csrf} canWrite={canWrite} pending={pending} />
        </div>)}
      </details>
    )}
  </main>
}
