env "local" {
  src = "file://internal/db/schema.sql"
  dev = "sqlite://dev?mode=memory"
  migration {
    dir = "file://internal/db/migrations"
  }
  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}
