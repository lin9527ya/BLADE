import type { ScheduleTaskSpec, TaskTemplate } from "./types";

// A schedule is a snapshot, not a pointer to future template edits. Preserve
// fields the simple editor does not expose, including future response fields.
export function editScheduleSpec(
  base: Partial<ScheduleTaskSpec>,
  description: string,
  goal: string,
  categoryID?: number,
): ScheduleTaskSpec {
  return { ...base, description: description.trim(), goal: goal.trim(), category_id: categoryID ?? null };
}

export function applyScheduleTemplate(
  base: Partial<ScheduleTaskSpec>,
  template: TaskTemplate,
): Partial<ScheduleTaskSpec> {
  return {
    ...base,
    description: template.description,
    goal: template.goal,
    category_id: template.category_id ?? base.category_id,
    intercept_rules: structuredClone(template.intercept_rules ?? []),
  };
}
