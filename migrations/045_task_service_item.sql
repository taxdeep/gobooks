-- 045_task_service_item.sql
-- Batch: Task Service Item link
--
-- Links a task to a specific service item from the Products & Services catalogue.
-- When set, the service item's revenue account and tax code are used when generating
-- the invoice draft line instead of the generic TASK_LABOR system item.
--
-- Column is nullable; existing tasks are unaffected (NULL = use TASK_LABOR default).

ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS product_service_id BIGINT
        REFERENCES product_services(id);

CREATE INDEX IF NOT EXISTS idx_tasks_product_service
    ON tasks (product_service_id)
    WHERE product_service_id IS NOT NULL;
