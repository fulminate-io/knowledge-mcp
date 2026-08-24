/* The dispatch table C uses in place of a supertype construct: a struct of
 * function pointers, filled by a composite literal. Its fields are the slots a
 * slot-bind edge runs from. */

struct http_conn;

struct http_ops {
  int (*flush)(struct http_conn *h);
  int (*close)(struct http_conn *h);
  int version;
};

struct http_conn {
  struct http_ops *ops;
  int fd;
};
