-- Project chats are each user's own now, keyed "<project id>:<user id>". The
-- shared ones from before (keyed by the project id alone) belong to nobody
-- in particular and are dropped.
DELETE FROM chat_messages WHERE kind = 'project' AND instr(owner_id, ':') = 0;
