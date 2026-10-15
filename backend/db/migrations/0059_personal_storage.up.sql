SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE wellspiking_personal_bindings (
 owner_id TEXT PRIMARY KEY REFERENCES users(id),
 id TEXT NOT NULL UNIQUE CHECK (id <> ''),
 username TEXT NOT NULL CHECK (username ~ '^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$'),
 secret_name TEXT NOT NULL UNIQUE CHECK (secret_name <> ''),
 public_key TEXT NOT NULL CHECK (public_key <> ''),
 fingerprint TEXT NOT NULL CHECK (fingerprint <> ''),
 generation BIGINT NOT NULL CHECK (generation = 1),
 state TEXT NOT NULL CHECK (state IN ('PENDING', 'READY')),
 verified_at TIMESTAMPTZ,
 created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
 UNIQUE (username),
 CHECK ((state = 'READY' AND verified_at IS NOT NULL) OR state = 'PENDING')
);

CREATE FUNCTION wellspiking_personal_binding_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF ROW(NEW.owner_id, NEW.id, NEW.username, NEW.secret_name, NEW.public_key, NEW.fingerprint, NEW.generation, NEW.created_at)
  IS DISTINCT FROM ROW(OLD.owner_id, OLD.id, OLD.username, OLD.secret_name, OLD.public_key, OLD.fingerprint, OLD.generation, OLD.created_at) THEN
  RAISE EXCEPTION 'personal storage binding identity is immutable';
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER wellspiking_personal_binding_identity BEFORE UPDATE ON wellspiking_personal_bindings FOR EACH ROW EXECUTE FUNCTION wellspiking_personal_binding_identity();
