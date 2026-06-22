module Mercator
  class Error < StandardError
    attr_reader :status_code, :code, :details, :response

    def initialize(status_code, code, message, details: nil, response: nil)
      super(message)
      @error_message = message
      @status_code = status_code
      @code = code
      @details = details
      @response = response
    end

    def to_s
      if status_code.nil?
        "#{code}: #{@error_message}"
      else
        "Mercator API error #{status_code} #{code}: #{@error_message}"
      end
    end

    def message
      @error_message
    end
  end
end
