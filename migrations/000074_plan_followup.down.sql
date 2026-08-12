DELETE FROM prompt_templates WHERE name = 'intent_classifier_plan_followup';
ALTER TABLE turns DROP COLUMN IF EXISTS answer_only;
