BEGIN;

UPDATE provider_catalog
SET exec_secret_id = CASE id
  WHEN 'brave' THEN 'search-brave-api-key'
  WHEN 'tavily' THEN 'search-tavily-api-key'
  WHEN 'exa' THEN 'search-exa-api-key'
  WHEN 'gemini-search' THEN 'search-gemini-api-key'
  WHEN 'firecrawl' THEN 'search-firecrawl-api-key'
  ELSE exec_secret_id
END
WHERE category = 'search'
  AND id IN ('brave', 'tavily', 'exa', 'gemini-search', 'firecrawl');

COMMIT;
