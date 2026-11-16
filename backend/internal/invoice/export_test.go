package invoice

// SetExpireBatch changes how many invoices one ExpireDue transaction
// handles, so tests can exercise paging with a few rows.
func (s *Service) SetExpireBatch(n int) { s.expireBatch = max(n, 1) }
