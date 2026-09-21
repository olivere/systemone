package systemone

import "maps"

func copyFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	return new(*p)
}

func cloneRequest(req Request) Request {
	req.State = append(req.State[:0:0], req.State...)
	req.Questions = append([]QuestionSpec(nil), req.Questions...)
	for i := range req.Questions {
		req.Questions[i].Options = append([]Option[string](nil), req.Questions[i].Options...)
		req.Questions[i].Levels = append([]string(nil), req.Questions[i].Levels...)
		req.Questions[i].LevelContents = append([]Content(nil), req.Questions[i].LevelContents...)
	}
	return req
}

func cloneResponse(response Response) Response {
	response.Answers = maps.Clone(response.Answers)
	for key, answer := range response.Answers {
		if answer.Choice != nil {
			c := *answer.Choice
			c.Probabilities = maps.Clone(c.Probabilities)
			c.Confidence = copyFloat(c.Confidence)
			answer.Choice = &c
		}
		if answer.Score != nil {
			s := *answer.Score
			s.Probabilities = append([]float64(nil), s.Probabilities...)
			s.Confidence = copyFloat(s.Confidence)
			answer.Score = &s
		}
		if answer.Noul != nil {
			answer.Noul = new(*answer.Noul)
		}
		response.Answers[key] = answer
	}
	return response
}
