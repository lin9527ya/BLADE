ALTER TABLE tool_executions ADD COLUMN IF NOT EXISTS logical_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tool_executions ADD COLUMN IF NOT EXISTS execution_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE tool_executions ADD COLUMN IF NOT EXISTS execution_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS logical_session_id TEXT NOT NULL DEFAULT '';
ALTER TABLE exploration_nodes ADD COLUMN IF NOT EXISTS execution_generation BIGINT NOT NULL DEFAULT 0;

-- Graph changes publish only after business writes, in transaction commit order.
CREATE TABLE IF NOT EXISTS blackboard_versions (
 exploration_id BIGINT PRIMARY KEY,
 epoch TEXT NOT NULL DEFAULT md5(random()::text || clock_timestamp()::text),
 revision BIGINT NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS blackboard_operations (
 id TEXT PRIMARY KEY, exploration_id BIGINT NOT NULL, reason_code TEXT NOT NULL,
 detail TEXT NOT NULL DEFAULT '', created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS blackboard_outbox (
 id BIGSERIAL PRIMARY KEY, transaction_id BIGINT NOT NULL,
 exploration_id BIGINT NOT NULL, entity TEXT NOT NULL, object_key TEXT NOT NULL,
 operation TEXT NOT NULL, node_id BIGINT, src_id BIGINT, dst_id BIGINT, relation TEXT,
 old_state TEXT, new_state TEXT, content_version BIGINT,
 changed_fields TEXT[] NOT NULL DEFAULT '{}', operation_id TEXT, reason_code TEXT
);
CREATE INDEX IF NOT EXISTS idx_blackboard_outbox_tx ON blackboard_outbox(transaction_id,exploration_id,id);
CREATE TABLE IF NOT EXISTS blackboard_events (
 exploration_id BIGINT NOT NULL, seq BIGINT NOT NULL, epoch TEXT NOT NULL,
 entity TEXT NOT NULL, object_key TEXT NOT NULL, operation TEXT NOT NULL,
 node_id BIGINT, src_id BIGINT, dst_id BIGINT, relation TEXT,
 old_state TEXT, new_state TEXT, content_version BIGINT,
 changed_fields TEXT[] NOT NULL DEFAULT '{}', operation_id TEXT, reason_code TEXT,
 created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(exploration_id,seq)
);
INSERT INTO blackboard_versions(exploration_id) SELECT id FROM explorations ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION blackboard_init_version() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 INSERT INTO blackboard_versions(exploration_id) VALUES(NEW.id) ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='trg_blackboard_init' AND tgrelid='explorations'::regclass) THEN
   CREATE TRIGGER trg_blackboard_init AFTER INSERT ON explorations FOR EACH ROW EXECUTE FUNCTION blackboard_init_version();
 END IF;
END $$;

-- Route child deletions before their parent disappears. No body or credential is duplicated.
ALTER TABLE exploration_anchors ADD COLUMN IF NOT EXISTS exploration_id BIGINT;
UPDATE exploration_anchors a SET exploration_id=n.exploration_id FROM exploration_nodes n WHERE a.node_id=n.id AND a.exploration_id IS NULL;
ALTER TABLE findings ADD COLUMN IF NOT EXISTS exploration_id BIGINT;
UPDATE findings f SET exploration_id=t.exploration_id FROM tasks t WHERE f.task_id=t.id AND f.exploration_id IS NULL;
UPDATE findings f SET exploration_id=n.exploration_id FROM exploration_nodes n WHERE f.node_id=n.id AND f.exploration_id IS NULL;
ALTER TABLE finding_traffic_bindings ADD COLUMN IF NOT EXISTS exploration_id BIGINT;
UPDATE finding_traffic_bindings b SET exploration_id=f.exploration_id FROM findings f WHERE b.finding_id=f.id AND b.exploration_id IS NULL;

CREATE OR REPLACE FUNCTION blackboard_route() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_TABLE_NAME='exploration_anchors' THEN
   SELECT exploration_id INTO NEW.exploration_id FROM exploration_nodes WHERE id=NEW.node_id;
 ELSIF TG_TABLE_NAME='findings' THEN
   IF NEW.task_id IS NOT NULL THEN SELECT exploration_id INTO NEW.exploration_id FROM tasks WHERE id=NEW.task_id;
   ELSIF NEW.node_id IS NOT NULL THEN SELECT exploration_id INTO NEW.exploration_id FROM exploration_nodes WHERE id=NEW.node_id;
   END IF;
 ELSIF TG_TABLE_NAME='finding_traffic_bindings' THEN
   SELECT exploration_id INTO NEW.exploration_id FROM findings WHERE id=NEW.finding_id;
 END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION blackboard_capture() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE before_row JSONB; after_row JSONB; r JSONB; old_exp BIGINT; new_exp BIGINT; e BIGINT;
 entity_kind TEXT; obj TEXT; n BIGINT; src BIGINT; dst BIGINT; rel TEXT; fields TEXT[];
BEGIN
 IF current_setting('blade.graph_migration',true)='on' THEN RETURN NULL; END IF;
 IF current_setting('blade.writer_protocol',true) IS DISTINCT FROM '2' THEN
   RAISE EXCEPTION 'blackboard writer protocol 2 required; stop old writers before upgrading' USING ERRCODE='55000';
 END IF;
 IF current_setting('blade.graph_tx_sealed',true)='on' THEN
   RAISE EXCEPTION 'graph transaction already published; no writes after deferred flush' USING ERRCODE='55000';
 END IF;
 IF TG_OP<>'INSERT' THEN before_row=to_jsonb(OLD); END IF;
 IF TG_OP<>'DELETE' THEN after_row=to_jsonb(NEW); END IF;
 IF TG_OP='UPDATE' AND (before_row - ARRAY['updated_at','heartbeat_at','lease_expires_at']) = (after_row - ARRAY['updated_at','heartbeat_at','lease_expires_at']) THEN RETURN NULL; END IF;
 r=COALESCE(after_row,before_row);
 old_exp=NULLIF(before_row->>'exploration_id','')::bigint;
 new_exp=NULLIF(after_row->>'exploration_id','')::bigint;
 IF TG_TABLE_NAME='explorations' THEN old_exp=NULLIF(before_row->>'id','')::bigint;new_exp=NULLIF(after_row->>'id','')::bigint; END IF;
 entity_kind=CASE TG_TABLE_NAME WHEN 'exploration_nodes' THEN 'node' WHEN 'exploration_edges' THEN 'edge' WHEN 'exploration_anchors' THEN 'anchor' WHEN 'findings' THEN 'finding' WHEN 'finding_traffic_bindings' THEN 'evidence_binding' WHEN 'completion_waivers' THEN 'waiver' WHEN 'task_constraints' THEN 'constraint' ELSE 'exploration' END;
 n=CASE WHEN entity_kind='node' THEN (r->>'id')::bigint ELSE NULLIF(r->>'node_id','')::bigint END;
 src=NULLIF(r->>'src_id','')::bigint;dst=NULLIF(r->>'dst_id','')::bigint;rel=r->>'rel';
 obj=CASE WHEN entity_kind='edge' THEN src::text||':'||rel||':'||dst::text WHEN entity_kind='anchor' THEN n::text||':'||(r->>'asset_id') ELSE COALESCE(r->>'id','') END;
 SELECT COALESCE(array_agg(k),'{}') INTO fields FROM unnest(ARRAY['payload','state','priority','owner','semantic_outcome','semantic_note','delete_reason','content_version','attrs','status','confidence','evidence_version','report_evidence_version','role','position','asset_ids','logical_session_id','execution_generation']) AS k WHERE before_row->k IS DISTINCT FROM after_row->k;
 FOR e IN SELECT DISTINCT exp FROM unnest(ARRAY[old_exp,new_exp]) AS exp WHERE exp IS NOT NULL LOOP
   INSERT INTO blackboard_outbox(transaction_id,exploration_id,entity,object_key,operation,node_id,src_id,dst_id,relation,old_state,new_state,content_version,changed_fields,operation_id,reason_code)
   VALUES(txid_current(),e,entity_kind,obj,lower(TG_OP),n,src,dst,rel,before_row->>'state',after_row->>'state',NULLIF(r->>'content_version','')::bigint,fields,NULLIF(current_setting('blade.graph_operation',true),''),NULLIF(current_setting('blade.graph_reason',true),''));
 END LOOP;
 RETURN NULL;
END $$;

CREATE OR REPLACE FUNCTION blackboard_flush() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE e BIGINT; r RECORD; v BIGINT; ep TEXT;
BEGIN
 IF NOT EXISTS(SELECT 1 FROM blackboard_outbox WHERE transaction_id=txid_current()) THEN RETURN NULL; END IF;
 -- All tasks in one transaction take version locks in the same order, after business locks.
 FOR e IN SELECT DISTINCT exploration_id FROM blackboard_outbox WHERE transaction_id=txid_current() ORDER BY exploration_id LOOP
   INSERT INTO blackboard_versions(exploration_id) VALUES(e) ON CONFLICT DO NOTHING;
   PERFORM 1 FROM blackboard_versions WHERE exploration_id=e FOR UPDATE;
 END LOOP;
 FOR r IN SELECT * FROM blackboard_outbox WHERE transaction_id=txid_current() ORDER BY exploration_id,id LOOP
   UPDATE blackboard_versions SET revision=revision+1 WHERE exploration_id=r.exploration_id RETURNING revision,epoch INTO v,ep;
   INSERT INTO blackboard_events(exploration_id,seq,epoch,entity,object_key,operation,node_id,src_id,dst_id,relation,old_state,new_state,content_version,changed_fields,operation_id,reason_code)
   VALUES(r.exploration_id,v,ep,r.entity,r.object_key,r.operation,r.node_id,r.src_id,r.dst_id,r.relation,r.old_state,r.new_state,r.content_version,r.changed_fields,r.operation_id,r.reason_code);
 END LOOP;
 DELETE FROM blackboard_outbox WHERE transaction_id=txid_current();
 PERFORM set_config('blade.graph_tx_sealed','on',true);
 RETURN NULL;
END $$;

DO $$ DECLARE t TEXT; BEGIN
 FOREACH t IN ARRAY ARRAY['exploration_anchors','findings','finding_traffic_bindings'] LOOP
   IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='trg_blackboard_route' AND tgrelid=t::regclass) THEN
     EXECUTE format('CREATE TRIGGER trg_blackboard_route BEFORE INSERT OR UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION blackboard_route()',t);
   END IF;
 END LOOP;
 FOREACH t IN ARRAY ARRAY['explorations','exploration_nodes','exploration_edges','exploration_anchors','findings','finding_traffic_bindings','completion_waivers','task_constraints'] LOOP
   IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='trg_blackboard_capture' AND tgrelid=t::regclass) THEN
     EXECUTE format('CREATE TRIGGER trg_blackboard_capture AFTER INSERT OR UPDATE OR DELETE ON %I FOR EACH ROW EXECUTE FUNCTION blackboard_capture()',t);
   END IF;
 END LOOP;
 IF NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgname='trg_blackboard_flush' AND tgrelid='blackboard_outbox'::regclass) THEN
   CREATE CONSTRAINT TRIGGER trg_blackboard_flush AFTER INSERT ON blackboard_outbox DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION blackboard_flush();
 END IF;
END $$;

CREATE OR REPLACE FUNCTION blackboard_time(value TEXT) RETURNS timestamptz LANGUAGE plpgsql STABLE AS $$
BEGIN RETURN value::timestamptz; EXCEPTION WHEN OTHERS THEN RETURN NULL; END $$;

CREATE OR REPLACE FUNCTION blackboard_requirement_matches(p JSONB,st TEXT,kind TEXT,r JSONB) RETURNS boolean LANGUAGE plpgsql STABLE AS $$
DECLARE observed timestamptz; deadline timestamptz; target JSONB; age BIGINT;
BEGIN
 IF r->>'type' IN ('fact','capability') AND kind='fact' AND st<>'confirmed' THEN RETURN false; END IF;
 IF kind IN ('credential','foothold') AND st<>'active' THEN RETURN false; END IF;
 IF r->>'type'='capability' AND kind='fact' AND p->>'polarity' IS DISTINCT FROM 'positive' THEN RETURN false; END IF;
 IF COALESCE(r->>'polarity','any')<>'any' AND COALESCE(p->>'polarity','')<>r->>'polarity' THEN RETURN false; END IF;
 observed=blackboard_time(p->'evidence_meta'->>'observed_at');
 deadline=blackboard_time(p->'evidence_meta'->>'valid_until');
 IF deadline IS NOT NULL AND deadline<=now() THEN RETURN false; END IF;
 IF COALESCE(r->>'max_age_seconds','') ~ '^[0-9]+$' THEN
   age=(r->>'max_age_seconds')::bigint;
   IF age>0 AND NOT (r->>'type'='credential' AND COALESCE(r->>'verification','false')='true') AND (observed IS NULL OR observed<now()-(age*interval '1 second')) THEN RETURN false; END IF;
 END IF;
 IF r->>'type'='credential' AND r->>'verification'='true' THEN
   FOR target IN SELECT * FROM jsonb_array_elements(CASE WHEN jsonb_typeof(p->'targets')='array' THEN p->'targets' ELSE '[]'::jsonb END) LOOP
     IF target->>'host'=r->>'target' AND target->>'verified'='true' AND (NOT (target ? 'valid_until') OR target->>'valid_until'='' OR blackboard_time(target->>'valid_until')>now()) THEN
       IF COALESCE(r->>'max_age_seconds','0')='0' OR (target ? 'observed_at' AND blackboard_time(target->>'observed_at')>now()-((r->>'max_age_seconds')::bigint*interval '1 second')) THEN RETURN true; END IF;
     END IF;
   END LOOP;
   RETURN false;
 END IF;
 RETURN true;
EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN RETURN false;
END $$;

CREATE OR REPLACE FUNCTION blackboard_requirement_issues(p JSONB,exp BIGINT) RETURNS JSONB LANGUAGE plpgsql STABLE AS $$
DECLARE r JSONB; ok BOOLEAN; issues JSONB='[]'::jsonb; ordinal INT=0;
BEGIN
 IF NOT (p ? 'requires') THEN RETURN issues; END IF;
 IF jsonb_typeof(p->'requires')<>'array' THEN RETURN '["invalid_requires"]'::jsonb; END IF;
 FOR r IN SELECT * FROM jsonb_array_elements(p->'requires') LOOP
   ordinal=ordinal+1;
   IF jsonb_typeof(r)<>'object' OR COALESCE(r->>'type','') NOT IN ('fact','capability','credential')
     OR (r ? 'node_id' AND COALESCE(r->>'node_id','') !~ '^[1-9][0-9]{0,17}$')
     OR (NOT (r ? 'node_id') AND COALESCE(r->>'key','')='')
     OR (r ? 'max_age_seconds' AND (COALESCE(r->>'max_age_seconds','') !~ '^[0-9]{1,8}$' OR (r->>'max_age_seconds')::numeric>31536000))
     OR (r ? 'verification' AND r->>'verification' NOT IN ('true','false'))
     OR (r->>'verification'='true' AND (r->>'type'<>'credential' OR COALESCE(r->>'target','')='')) THEN
      issues=issues||jsonb_build_array(jsonb_build_object('index',ordinal,'reason','invalid_requirement'));CONTINUE;
   END IF;
   SELECT EXISTS(SELECT 1 FROM exploration_nodes n
      WHERE n.exploration_id=exp
      AND ((r->>'type'='fact' AND n.kind='fact') OR (r->>'type'='capability' AND n.kind IN ('fact','foothold','credential')) OR (r->>'type'='credential' AND n.kind='credential'))
      AND ((COALESCE(r->>'node_id','') ~ '^[0-9]+$' AND n.id=(r->>'node_id')::bigint)
        OR (NOT (r ? 'node_id') AND COALESCE(r->>'key','')<>'' AND (n.payload->'evidence_meta'->>'claim_key'=r->>'key' OR COALESCE(n.payload->'enables','[]'::jsonb) ? (r->>'key'))))
      AND NOT EXISTS(SELECT 1 FROM exploration_edges e WHERE e.exploration_id=exp AND ((e.rel='supersedes' AND e.dst_id=n.id) OR (e.rel='contradicts' AND (e.src_id=n.id OR e.dst_id=n.id))))
      AND blackboard_requirement_matches(n.payload,n.state,n.kind,r)) INTO ok;
   IF NOT ok THEN issues=issues||jsonb_build_array(jsonb_build_object('index',ordinal,'type',r->>'type','node_id',r->>'node_id','key',r->>'key','reason','missing_stale_unverified_or_contested')); END IF;
 END LOOP;
 RETURN issues;
EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN RETURN '["invalid_requires"]'::jsonb;
END $$;
CREATE OR REPLACE FUNCTION blackboard_intent_ready(p JSONB,exp BIGINT) RETURNS boolean LANGUAGE SQL STABLE AS $$ SELECT jsonb_array_length(blackboard_requirement_issues(p,exp))=0 $$;
