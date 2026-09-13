package repositories

func NewModelServingForJobs(jobs *GormRepository) *ModelServingRepository {
	if jobs == nil {
		return nil
	}
	return NewModelServingRepository(jobs.db)
}
