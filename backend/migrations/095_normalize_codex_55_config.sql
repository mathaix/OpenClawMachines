-- Normalize persisted Codex 5.5 config after the runtime provider namespace
-- changed from openai/ to codex/.
UPDATE model_catalog
SET gateway_model_id = 'codex/gpt-5.5'
WHERE id = 'openai-codex/gpt-5.5';

UPDATE machine_config
SET platform_overrides = jsonb_set(
    COALESCE(platform_overrides, '{}'::jsonb),
    '{preferred_model}',
    to_jsonb('openai-codex/gpt-5.5'::text),
    true
)
WHERE platform_overrides->>'preferred_model' = 'openai/gpt-5.5';

UPDATE machine_config
SET assembled_config = jsonb_set(
    jsonb_set(
        assembled_config,
        '{agents,defaults,model,primary}',
        to_jsonb('codex/gpt-5.5'::text),
        false
    ),
    '{agents,defaults,models}',
    (
        COALESCE(assembled_config #> '{agents,defaults,models}', '{}'::jsonb)
        - 'openai/gpt-5.5'
        || jsonb_build_object(
            'codex/gpt-5.5',
            COALESCE(
                assembled_config #> '{agents,defaults,models,codex/gpt-5.5}',
                '{}'::jsonb
            )
            || jsonb_build_object('agentRuntime', jsonb_build_object('id', 'codex'))
        )
    ),
    false
)
WHERE assembled_config #>> '{agents,defaults,model,primary}' = 'openai/gpt-5.5';
