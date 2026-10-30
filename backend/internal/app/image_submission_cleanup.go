package app

// expireImageSubmissionRequests compacts old image replay records.  The
// request body can contain large inline media, so only the repository's
// projection is loaded and the payload is cleared in bounded batches.  Query
// identity is retained when already available; providers that support a
// follow-up query can continue to use that compact record.
func (s *Service) expireImageSubmissionRequests() error {
	if s == nil || s.repo == nil {
		return nil
	}
	for {
		rows, err := s.repo.ExpiredImageSubmissionRequests()
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := s.repo.ExpireImageSubmissionRequest(row, row.QueryCipher); err != nil {
				return err
			}
		}
		if len(rows) < 100 {
			return nil
		}
	}
}
