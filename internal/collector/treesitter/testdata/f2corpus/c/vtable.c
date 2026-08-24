/* Both vtable-filling shapes, side by side, plus the known-negatives the
 * slot-bind arm must decline: a plain data field, a NULL-valued slot, and an
 * initializer whose target is not an identifier. */

#include "ops.h"

static int real_flush(struct http_conn *h) {
  return h->fd;
}

static int real_close(struct http_conn *h) {
  return h->fd;
}

/* DESIGNATED: each pair names its field outright. `version` is a plain data
 * field and `close` is NULL, so neither is a dispatch target. */
static struct http_ops designated_ops = {
    .flush = real_flush,
    .close = NULL,
    .version = 2,
};

/* POSITIONAL: slot order is the field order, which is why the declaration's
 * field order has to travel with the binds. */
static struct http_ops positional_ops = {
    &real_flush,
    real_close,
    3,
};

void drive(struct http_conn *c) {
  c->ops->flush(c);
}

void call_value(struct http_ops *ops, struct http_conn *h) {
  ops->close(h);
}
